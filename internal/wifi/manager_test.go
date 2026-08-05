package wifi

import (
	"context"
	"testing"
)

type fakeBackend struct {
	startHotspotCalls int
	stopHotspotCalls  int
	connectCalls      int
	startErr          error
	stopErr           error
	connectErr        error
	// stopHotspotBeforeConnect records, at the moment Connect is called,
	// whether StopHotspot had already been called an earlier time --
	// lets tests assert the teardown-before-handoff ordering directly.
	stopHotspotBeforeConnect int
}

func (f *fakeBackend) Name() string                            { return "fake" }
func (f *fakeBackend) Scan(context.Context) ([]Network, error) { return nil, nil }
func (f *fakeBackend) ListKnownNetworks(context.Context) ([]string, error) {
	return nil, nil
}
func (f *fakeBackend) Connect(context.Context, string, string) error {
	f.connectCalls++
	f.stopHotspotBeforeConnect = f.stopHotspotCalls
	return f.connectErr
}
func (f *fakeBackend) Status(context.Context) (Status, error) { return Status{}, nil }
func (f *fakeBackend) StartHotspot(context.Context, string, string) error {
	f.startHotspotCalls++
	return f.startErr
}
func (f *fakeBackend) StopHotspot(context.Context) error {
	f.stopHotspotCalls++
	return f.stopErr
}

// newTestManager builds a Manager wired to a fakeBackend and stubbed
// hasRoute/startCaptivePortal, so the state machine can be driven
// deterministically without any real network state or actually binding
// port 80 on whatever machine runs the test suite.
func newTestManager(hasRoute bool) (*Manager, *fakeBackend) {
	fb := &fakeBackend{}
	m := NewManager("test-ssid", "test-password", "8088", true)
	m.SetBackend(fb)
	m.hasRoute = func(context.Context, string) (bool, error) { return hasRoute, nil }
	m.startCaptivePortal = func(string) *captivePortal { return nil }
	return m, fb
}

func TestCheckAndActStartsHotspotWhenNoRoute(t *testing.T) {
	m, fb := newTestManager(false)
	m.checkAndAct(context.Background())
	if fb.startHotspotCalls != 1 {
		t.Errorf("StartHotspot calls = %d, want 1", fb.startHotspotCalls)
	}
	if fb.stopHotspotCalls != 0 {
		t.Errorf("StopHotspot calls = %d, want 0", fb.stopHotspotCalls)
	}
	if !m.hotspotActive {
		t.Error("hotspotActive = false, want true after a successful StartHotspot")
	}
}

func TestCheckAndActStopsHotspotWhenRouteReturns(t *testing.T) {
	m, fb := newTestManager(true)
	m.hotspotActive = true
	m.checkAndAct(context.Background())
	if fb.stopHotspotCalls != 1 {
		t.Errorf("StopHotspot calls = %d, want 1", fb.stopHotspotCalls)
	}
	if fb.startHotspotCalls != 0 {
		t.Errorf("StartHotspot calls = %d, want 0", fb.startHotspotCalls)
	}
	if m.hotspotActive {
		t.Error("hotspotActive = true, want false after a successful StopHotspot")
	}
}

func TestCheckAndActNoOpWhenAlreadyConnected(t *testing.T) {
	m, fb := newTestManager(true)
	m.checkAndAct(context.Background())
	if fb.startHotspotCalls != 0 || fb.stopHotspotCalls != 0 {
		t.Errorf("expected no backend calls, got start=%d stop=%d", fb.startHotspotCalls, fb.stopHotspotCalls)
	}
}

func TestCheckAndActNoOpWhenAlreadyHotspot(t *testing.T) {
	m, fb := newTestManager(false)
	m.hotspotActive = true
	m.checkAndAct(context.Background())
	if fb.startHotspotCalls != 0 || fb.stopHotspotCalls != 0 {
		t.Errorf("expected no backend calls, got start=%d stop=%d", fb.startHotspotCalls, fb.stopHotspotCalls)
	}
}

func TestCheckAndActHoldsOffDuringConnectGracePeriod(t *testing.T) {
	m, fb := newTestManager(false)
	m.NotifyConnectAttempt()
	m.checkAndAct(context.Background())
	if fb.startHotspotCalls != 0 {
		t.Errorf("StartHotspot calls = %d, want 0 during the grace period", fb.startHotspotCalls)
	}
}

func TestCheckAndActDisabledIsAlwaysNoOp(t *testing.T) {
	m, fb := newTestManager(false)
	m.enabled = false
	m.checkAndAct(context.Background())
	if fb.startHotspotCalls != 0 || fb.stopHotspotCalls != 0 {
		t.Errorf("expected no backend calls with enabled=false, got start=%d stop=%d", fb.startHotspotCalls, fb.stopHotspotCalls)
	}
}

func TestCheckAndActStartFailureLeavesHotspotActiveFalse(t *testing.T) {
	m, fb := newTestManager(false)
	fb.startErr = context.DeadlineExceeded
	m.checkAndAct(context.Background())
	if m.hotspotActive {
		t.Error("hotspotActive = true after a failed StartHotspot, want false")
	}
}

// TestCheckAndActBacksOffAfterFailedHotspotAttempt is the direct
// regression test for a real incident: a StartHotspot that fails every
// time (e.g. a driver that doesn't actually support the hotspot's AP
// mode) got retried on every single tick, forever, each attempt
// stopping wpa_supplicant on its way in and never successfully
// bringing anything back up -- repeatedly cutting off the very
// connectivity this watchdog exists to recover.
func TestCheckAndActBacksOffAfterFailedHotspotAttempt(t *testing.T) {
	m, fb := newTestManager(false)
	fb.startErr = context.DeadlineExceeded
	m.checkAndAct(context.Background())
	m.checkAndAct(context.Background())
	m.checkAndAct(context.Background())
	if fb.startHotspotCalls != 1 {
		t.Errorf("StartHotspot calls = %d, want 1 -- repeated immediate calls should be held off by hotspotRetryBackoff", fb.startHotspotCalls)
	}
}

// TestCheckAndActExcludesWlan0RouteWhileHotspotActive is the direct
// regression test for a real incident: the fallback hotspot came up, a
// phone associated and got a DHCP lease, and the very next watchdog
// tick tore the hotspot back down again -- traced to a default route
// appearing via wlan0 itself (see route.go's own doc comment). This
// confirms checkAndAct actually asks hasRoute to ignore wlan0 while the
// hotspot it stood up is the thing running there.
func TestCheckAndActExcludesWlan0RouteWhileHotspotActive(t *testing.T) {
	m, _ := newTestManager(false)
	var gotExclude string
	m.hasRoute = func(_ context.Context, excludeIface string) (bool, error) {
		gotExclude = excludeIface
		return true, nil
	}
	m.hotspotActive = true
	m.checkAndAct(context.Background())
	if gotExclude != wlan0Iface {
		t.Errorf("excludeIface = %q, want %q while hotspotActive", gotExclude, wlan0Iface)
	}
}

// TestCheckAndActDoesNotExcludeWlan0RouteWhenHotspotInactive makes sure
// the exclusion in the test above is conditional, not blanket -- a real
// client-mode WiFi connection via wlan0 (no hotspot running) must still
// count as a genuine route, or the watchdog would never recognize a
// successful Connect() and would keep fighting it every tick.
func TestCheckAndActDoesNotExcludeWlan0RouteWhenHotspotInactive(t *testing.T) {
	m, _ := newTestManager(false)
	gotExclude := "sentinel-should-be-overwritten"
	m.hasRoute = func(_ context.Context, excludeIface string) (bool, error) {
		gotExclude = excludeIface
		return false, nil
	}
	m.hotspotActive = false
	m.checkAndAct(context.Background())
	if gotExclude != "" {
		t.Errorf("excludeIface = %q, want \"\" when hotspot isn't active", gotExclude)
	}
}

// TestConnectTearsDownHotspotFirst is the direct regression test for a
// real incident: calling Backend().Connect() directly (bypassing
// Manager) started wpa_supplicant on wlan0 without ever stopping
// hostapd first, leaving both daemons fighting over the same physical
// radio -- confirmed on a real node, that knocked every joined client
// off the hotspot and never actually completed the requested connection
// either. Manager.Connect must call StopHotspot before Connect whenever
// the hotspot is active, and clear hotspotActive/captivePortal
// afterward.
func TestConnectTearsDownHotspotFirst(t *testing.T) {
	m, fb := newTestManager(false)
	m.hotspotActive = true
	m.captivePortal = nil // startCaptivePortal is stubbed to return nil in newTestManager

	if err := m.Connect(context.Background(), "MyNetwork", "hunter2"); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if fb.stopHotspotCalls != 1 {
		t.Errorf("StopHotspot calls = %d, want 1", fb.stopHotspotCalls)
	}
	if fb.connectCalls != 1 {
		t.Errorf("Connect calls = %d, want 1", fb.connectCalls)
	}
	if fb.stopHotspotBeforeConnect != 1 {
		t.Errorf("StopHotspot must be called before Connect, got stopHotspotCalls=%d at the moment Connect ran", fb.stopHotspotBeforeConnect)
	}
	if m.hotspotActive {
		t.Error("hotspotActive = true after Connect, want false")
	}
}

// TestConnectSkipsTeardownWhenHotspotInactive makes sure the teardown
// above is conditional -- an ordinary Connect (no hotspot running) must
// not call StopHotspot at all.
func TestConnectSkipsTeardownWhenHotspotInactive(t *testing.T) {
	m, fb := newTestManager(false)
	m.hotspotActive = false

	if err := m.Connect(context.Background(), "MyNetwork", "hunter2"); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if fb.stopHotspotCalls != 0 {
		t.Errorf("StopHotspot calls = %d, want 0 when the hotspot wasn't active", fb.stopHotspotCalls)
	}
	if fb.connectCalls != 1 {
		t.Errorf("Connect calls = %d, want 1", fb.connectCalls)
	}
}

// TestConnectSetsGracePeriodBeforeCallingBackend makes sure the
// watchdog's connect grace period is armed before the (possibly slow)
// backend.Connect call, not after -- otherwise a watchdog tick landing
// mid-attempt could race it and try to stand the hotspot back up while
// a connection attempt is still in flight.
func TestConnectSetsGracePeriodBeforeCallingBackend(t *testing.T) {
	m, _ := newTestManager(false)
	if !m.lastConnectAttempt.IsZero() {
		t.Fatal("test setup: lastConnectAttempt should start zero")
	}
	if err := m.Connect(context.Background(), "MyNetwork", "hunter2"); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if m.lastConnectAttempt.IsZero() {
		t.Error("lastConnectAttempt was not set by Connect")
	}
}
