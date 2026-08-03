package wifi

import (
	"context"
	"testing"
)

type fakeBackend struct {
	startHotspotCalls int
	stopHotspotCalls  int
	startErr          error
	stopErr           error
}

func (f *fakeBackend) Name() string                                  { return "fake" }
func (f *fakeBackend) Scan(context.Context) ([]Network, error)       { return nil, nil }
func (f *fakeBackend) Connect(context.Context, string, string) error { return nil }
func (f *fakeBackend) Status(context.Context) (Status, error)        { return Status{}, nil }
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
	m.hasRoute = func(context.Context) (bool, error) { return hasRoute, nil }
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
