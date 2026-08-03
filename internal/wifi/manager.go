package wifi

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

const (
	// wifiWatchdogInterval matches linkHistoryInterval's own interval
	// choice in internal/server/linkhistory.go.
	wifiWatchdogInterval = 30 * time.Second

	// connectGracePeriod is how long after an explicit Connect() call
	// the watchdog holds off flipping into hotspot mode even if no
	// default route exists yet -- covers the real window where DHCP on
	// a freshly associated wlan0 is still in flight. Without this, a
	// Connect() that takes longer than one tick to actually get an IP
	// would race the very next watchdog tick and undo itself.
	connectGracePeriod = 45 * time.Second

	// hotspotRetryBackoff bounds how often a failing StartHotspot gets
	// retried -- confirmed the hard way on real hardware: without this,
	// a StartHotspot that fails every time (e.g. hostapd's nl80211
	// driver not actually working on this WiFi chipset) got retried
	// every single wifiWatchdogInterval tick, forever, each attempt
	// stopping wpa_supplicant on its way in -- repeatedly cutting off
	// the very connectivity this watchdog exists to recover.
	hotspotRetryBackoff = 2 * time.Minute
)

// Manager owns this node's wlan0 hotspot-fallback state machine:
// bring up a fallback hotspot the moment there's no active network
// connection at all, and drop it again the moment a real connection
// comes back. See checkAndAct's own doc comment for the exact state
// transitions.
type Manager struct {
	mu                 sync.Mutex
	backend            Backend
	hotspotSSID        string
	hotspotPSK         string
	dashboardURL       string
	enabled            bool
	hotspotActive      bool
	lastConnectAttempt time.Time
	lastHotspotAttempt time.Time
	captivePortal      *captivePortal

	// hasRoute is defaultRouteExists by default -- overridable so tests
	// can drive the state machine without real network state, same
	// "parameterized for testability while the public function isn't"
	// shape as system.listNetworkInterfaces vs its own exported
	// ListNetworkInterfaces wrapper. The string arg is the interface to
	// exclude from consideration -- see defaultRouteExists's own doc
	// comment for why checkAndAct passes wlan0 while the hotspot is
	// active.
	hasRoute func(ctx context.Context, excludeIface string) (bool, error)

	// startCaptivePortal is the package-level startCaptivePortal func by
	// default -- overridable for the same reason as hasRoute: without
	// this, a test driving checkAndAct through a successful StartHotspot
	// would actually try to bind real port 80 on whatever machine runs
	// the test suite.
	startCaptivePortal func(dashboardURL string) *captivePortal
}

// NewManager builds a Manager with backend = unavailableBackend{} --
// SetBackend swaps in the real detected backend later (see
// (*server.Server).StartWiFiWatchdog), so constructing a Manager never
// shells out, matching the same "constructing a Server in tests
// doesn't shell out" reasoning already established for
// StartLinkHistoryPoller. dashboardPort is this app's own -addr port
// (e.g. "8088"), used to build the URL the captive-portal redirect
// server (see captive_portal.go) points phones/laptops at once they
// join the hotspot -- always http://hotspotStaticIP:<dashboardPort>/,
// since that's the one address guaranteed reachable from a device that
// just joined the hotspot and nothing else.
func NewManager(hotspotSSID, hotspotPSK, dashboardPort string, enabled bool) *Manager {
	return &Manager{
		backend:            unavailableBackend{},
		hotspotSSID:        hotspotSSID,
		hotspotPSK:         hotspotPSK,
		dashboardURL:       "http://" + hotspotStaticIP + ":" + dashboardPort + "/",
		enabled:            enabled,
		hasRoute:           defaultRouteExists,
		startCaptivePortal: startCaptivePortal,
	}
}

func (m *Manager) SetBackend(b Backend) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backend = b
}

func (m *Manager) Backend() Backend {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.backend
}

// HotspotSSID is the fixed SSID this node broadcasts as its fallback
// hotspot -- shown on the System page so an operator knows what to
// join.
func (m *Manager) HotspotSSID() string { return m.hotspotSSID }

// NotifyConnectAttempt records that a Connect was just requested --
// see checkAndAct's own doc comment for why this holds off the
// watchdog briefly afterward.
func (m *Manager) NotifyConnectAttempt() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastConnectAttempt = time.Now()
}

// Connect hands wlan0 over to ssid/psk, tearing down the fallback
// hotspot first if it's currently active -- the one correct entry
// point for an operator-requested connection. Calling
// Backend().Connect() directly instead (as this method used to be
// bypassed in favor of) starts wpa_supplicant on wlan0 without ever
// stopping hostapd first, leaving both daemons fighting over the same
// physical radio. Confirmed on a real node: that conflict knocked every
// joined client off the hotspot and never actually completed the
// requested connection either, leaving wlan0 stranded
// (wpa_state=DISCONNECTED) until the watchdog's own next retry cycle
// picked up the pieces minutes later. NotifyConnectAttempt's own grace
// period is set here, before the (possibly slow) backend.Connect call,
// so a watchdog tick landing mid-attempt can't race it and try to
// stand the hotspot back up while this is still in flight.
func (m *Manager) Connect(ctx context.Context, ssid, psk string) error {
	m.mu.Lock()
	backend := m.backend
	hotspotActive := m.hotspotActive
	cp := m.captivePortal
	m.mu.Unlock()

	if hotspotActive {
		if err := backend.StopHotspot(ctx); err != nil {
			return fmt.Errorf("tearing down fallback hotspot before connecting: %w", err)
		}
		cp.stop()
		m.mu.Lock()
		m.hotspotActive = false
		m.captivePortal = nil
		m.mu.Unlock()
	}

	m.NotifyConnectAttempt()
	return backend.Connect(ctx, ssid, psk)
}

// Run blocks until ctx is cancelled, checking connectivity every
// wifiWatchdogInterval and driving the hotspot-fallback state machine
// -- same shape as (*cloudagent.Agent).Run(ctx): a single supervised
// goroutine for the life of the process, started once from
// (*server.Server).StartWiFiWatchdog.
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(wifiWatchdogInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkAndAct(ctx)
		}
	}
}

// checkAndAct is Manager's whole hotspot-fallback state machine:
//   - no default route, not already broadcasting the hotspot, and no
//     Connect() requested within the last connectGracePeriod -> bring
//     the hotspot up.
//   - a default route exists and the hotspot is currently active ->
//     tear it down, handing wlan0 back to normal client-mode use.
//   - anything else (including enabled=false) is a no-op.
func (m *Manager) checkAndAct(ctx context.Context) {
	m.mu.Lock()
	enabled := m.enabled
	backend := m.backend
	hotspotActive := m.hotspotActive
	lastConnectAttempt := m.lastConnectAttempt
	lastHotspotAttempt := m.lastHotspotAttempt
	ssid, psk := m.hotspotSSID, m.hotspotPSK
	dashboardURL := m.dashboardURL
	startCP := m.startCaptivePortal
	m.mu.Unlock()

	if !enabled {
		return
	}

	excludeIface := ""
	if hotspotActive {
		excludeIface = wlan0Iface
	}
	hasRoute, err := m.hasRoute(ctx, excludeIface)
	if err != nil {
		// Fail toward trying to help, not toward silently doing
		// nothing: if we can't even tell whether there's a route,
		// treat that the same as "no route".
		hasRoute = false
	}

	switch {
	case !hasRoute && !hotspotActive:
		if time.Since(lastConnectAttempt) < connectGracePeriod {
			return
		}
		if !lastHotspotAttempt.IsZero() && time.Since(lastHotspotAttempt) < hotspotRetryBackoff {
			return
		}
		m.mu.Lock()
		m.lastHotspotAttempt = time.Now()
		m.mu.Unlock()
		log.Printf("wifi: no network connection detected — starting fallback hotspot %q via %s", ssid, backend.Name())
		if err := backend.StartHotspot(ctx, ssid, psk); err != nil {
			log.Printf("wifi: failed to start fallback hotspot: %v", err)
		} else {
			log.Printf("wifi: fallback hotspot %q is up", ssid)
			cp := startCP(dashboardURL)
			m.mu.Lock()
			m.hotspotActive = true
			m.captivePortal = cp
			m.mu.Unlock()
		}
	case hasRoute && hotspotActive:
		log.Printf("wifi: network connection restored — tearing down fallback hotspot")
		if err := backend.StopHotspot(ctx); err != nil {
			log.Printf("wifi: failed to stop fallback hotspot: %v", err)
		} else {
			m.mu.Lock()
			cp := m.captivePortal
			m.hotspotActive = false
			m.captivePortal = nil
			m.mu.Unlock()
			cp.stop()
		}
	}
}
