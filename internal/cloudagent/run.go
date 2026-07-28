// Package cloudagent is this node's optional, off-by-default connection
// to the public cloud platform (a separate product — see its own repo's
// docs for the cloud side). When the operator opts in on the local
// "Cloud Sync" settings card with an API key generated on the cloud
// site, this package dials out to it (never the reverse — see
// SettingsStore's doc comment and this package's Run) and services
// relayed JSON actions.
//
// This is one of two HTTP-facing layers built on the same internal/*
// domain packages (internal/config, internal/system, ...) that
// internal/server also wraps for its local html UI — internal/server
// renders HTML for a session-authenticated LAN browser, cloudagent
// speaks JSON over a single outbound connection authenticated by API
// key. Both are thin; the actual business logic lives once, in
// internal/*. See internal/rptstatus for the parsing layer both share
// for app_rpt CLI output specifically.
//
// The action registry (see dispatch.go) is a fixed, explicitly
// enumerated allowlist, never a generic "call this internal method by
// name" dispatcher — a compromised cloud backend must never be able to
// construct an arbitrary command (see internal/system.AsteriskRX, whose
// free-form cmd string is exactly what no relayed action may expose
// directly).
package cloudagent

import (
	"context"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/coder/websocket"

	"hamvoipconfiggui/internal/config"
	"hamvoipconfiggui/internal/sounds"
	"hamvoipconfiggui/internal/soundschedule"
	"hamvoipconfiggui/internal/wxtone"
)

// Backoff/timing constants for Run's reconnect loop and heartbeat.
const (
	// disabledPollInterval is how often Run rechecks settings while the
	// feature is off or unconfigured, so turning it on takes effect
	// within a few seconds without a restart.
	disabledPollInterval = 5 * time.Second

	// initialBackoff/maxBackoff bound the reconnect delay after a
	// failed dial or a rejected hello. Reset to initialBackoff only
	// after a successful helloAck (see runOnce) — a rejected API key
	// should back off like any other repeated failure, not hammer the
	// cloud at a fixed 1s.
	initialBackoff = 1 * time.Second
	maxBackoff     = 60 * time.Second

	// helloTimeout bounds the hello/helloAck round trip.
	helloTimeout = 10 * time.Second

	// heartbeatInterval is how often the status event is pushed while
	// connected — see internal/server/live.go's liveHub doc comment for
	// why this is a separate, cheap tier from the (not-yet-built)
	// on-demand per-node live watch: an always-on push has to stay
	// small, since it's now traffic over someone's home uplink for
	// every connected device, all the time.
	heartbeatInterval = 20 * time.Second

	// maxRelayMessageBytes overrides coder/websocket's default 32KiB
	// per-message read limit — see runOnce's own comment on
	// conn.SetReadLimit for why that default is far too small.
	maxRelayMessageBytes = 16 * 1024 * 1024
)

// Agent holds this node's cloud connection state and the internal/*
// dependencies its action registry wraps. Constructed once by
// internal/server (see (*Server).StartCloudAgent) and run for the
// life of the process.
type Agent struct {
	settings       *SettingsStore
	cloudURL       string // fixed at construction; never operator-editable, see New's doc comment
	store          *config.Store
	asteriskBin    string
	live           *liveWatches
	sounds         *sounds.Store
	soundSchedule  *soundschedule.Store
	wxTones        *wxtone.Store
	skywarnDir     string
	sa818Tool      string
	sa818StatePath string
	audit          *auditWriter

	mu            sync.Mutex
	reload        chan struct{}
	activeConn    *websocket.Conn // set only while runOnce holds an open connection
	lastConnected time.Time       // zero until the first successful helloAck

	// ownerSubscriptionActive mirrors envelope.OwnerSubscriptionActive
	// from the most recent successful helloAck this process lifetime --
	// false if this process has never connected. Same "last known, not
	// live" semantics as lastConnected above: it isn't reset back to
	// false when Cloud Sync is disabled or the connection drops, only
	// ever updated on the next successful helloAck.
	ownerSubscriptionActive bool
}

// New builds an Agent. settingsPath is where the operator's API key
// and enabled flag are persisted (see SettingsStore). cloudURL is the
// one address this Agent will ever dial — fixed at build/deploy time
// (see cmd/hamvoip-gui/main.go's -cloud-url flag), never read from the
// operator-facing settings form or the settings file on disk: an
// operator can point their own API key at the wrong place by typo, but
// they can't point this binary at an arbitrary WebSocket endpoint.
// Every dependency through sa818StatePath is the exact same one (often
// the exact same *Store instance) internal/server.New already
// constructs, passed through rather than built twice — see
// (*server.Server).StartCloudAgent. auditLogPath is where every
// dispatched action is independently recorded (see audit.go); an empty
// path disables audit logging entirely.
func New(
	settingsPath string,
	cloudURL string,
	store *config.Store,
	asteriskBin string,
	soundsStore *sounds.Store,
	soundSchedule *soundschedule.Store,
	wxTones *wxtone.Store,
	skywarnDir string,
	sa818Tool string,
	sa818StatePath string,
	auditLogPath string,
) *Agent {
	return &Agent{
		settings:       NewSettingsStore(settingsPath),
		cloudURL:       cloudURL,
		store:          store,
		asteriskBin:    asteriskBin,
		live:           newLiveWatches(),
		sounds:         soundsStore,
		soundSchedule:  soundSchedule,
		wxTones:        wxTones,
		skywarnDir:     skywarnDir,
		sa818Tool:      sa818Tool,
		sa818StatePath: sa818StatePath,
		audit:          newAuditWriter(auditLogPath),
		reload:         make(chan struct{}),
	}
}

// LastConnected reports when this Agent's connection to the cloud last
// completed a successful hello handshake — the zero Time if it has
// never connected this process lifetime. Exposed for the local Cloud
// Sync settings card to show as a simple, honest liveness signal.
func (a *Agent) LastConnected() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastConnected
}

// OwnerSubscriptionActive reports whether, as of the most recent
// successful helloAck this process lifetime, this device's owner had a
// genuine active paid cloud subscription -- false if this process has
// never connected. See envelope.OwnerSubscriptionActive's own doc
// comment for exactly what "active" means. Exposed for the home page's
// promo card to swap its subscribe pitch for a thank-you once the
// operator has actually paid.
func (a *Agent) OwnerSubscriptionActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ownerSubscriptionActive
}

// Settings exposes the settings store so internal/server's Cloud Sync
// handlers can read/write it without this package depending on
// net/http.
func (a *Agent) Settings() *SettingsStore { return a.settings }

// Reload takes effect immediately — called after the operator saves new
// settings — in two ways: it wakes a currently-waiting Run loop (so
// enabling the feature, or fixing a bad API key, doesn't wait out
// disabledPollInterval/the current backoff delay), and it forcibly
// closes any currently-open connection. The second part matters even
// more than the first: without it, disabling Cloud Sync (or changing
// the API key) while already connected would only stop *future*
// reconnect attempts — the live session, dialed under the old settings,
// would keep running until it happened to drop on its own. Local/
// physical access to this node must always be able to cut the cloud
// connection immediately, regardless of what the cloud side thinks.
func (a *Agent) Reload() {
	a.mu.Lock()
	defer a.mu.Unlock()
	close(a.reload)
	a.reload = make(chan struct{})
	if a.activeConn != nil {
		_ = a.activeConn.Close(websocket.StatusNormalClosure, "settings changed")
	}
}

func (a *Agent) setActiveConn(conn *websocket.Conn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.activeConn = conn
}

// Run dials out to the fixed cloud URL (see New's doc comment) and
// services it until ctx is cancelled, reconnecting with exponential
// backoff (full jitter) on any failure. Never listens for or accepts
// inbound connections — the entire point of this design is that the
// node is reachable behind home NAT without any port forwarding, so
// this only ever dials out. Call once, from (*Server).StartCloudAgent.
func (a *Agent) Run(ctx context.Context) {
	backoff := time.Duration(initialBackoff)
	for {
		if ctx.Err() != nil {
			return
		}
		settings, err := a.settings.Load()
		if err != nil || !settings.Enabled || settings.APIKey == "" || a.cloudURL == "" {
			if !a.wait(ctx, disabledPollInterval) {
				return
			}
			continue
		}
		// Always the Agent's own fixed URL, never whatever a
		// settings.json on disk might contain — see New's doc comment.
		settings.CloudURL = a.cloudURL

		if a.runOnce(ctx, settings) {
			backoff = initialBackoff
		} else {
			backoff = nextBackoff(backoff)
		}
		if !a.wait(ctx, jitter(backoff)) {
			return
		}
	}
}

// wait blocks for d, or until ctx is cancelled or Reload wakes it early.
// Returns false if ctx was cancelled (the caller should stop, not
// continue its loop).
func (a *Agent) wait(ctx context.Context, d time.Duration) bool {
	a.mu.Lock()
	reload := a.reload
	a.mu.Unlock()
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	case <-reload:
		return true
	}
}

func nextBackoff(cur time.Duration) time.Duration {
	next := cur * 2
	if next > maxBackoff {
		next = maxBackoff
	}
	return next
}

// jitter returns a duration uniformly distributed in [0, d) — "full
// jitter", so many devices reconnecting after a shared cloud-side blip
// don't all retry in lockstep.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(d)))
}

// logf is a small indirection so tests can silence/capture logging
// later if needed; today it's just log.Printf with a fixed prefix.
func logf(format string, args ...any) {
	log.Printf("cloudagent: "+format, args...)
}
