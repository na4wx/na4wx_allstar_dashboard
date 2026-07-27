package cloudagent

import (
	"context"
	"testing"

	"hamvoipconfiggui/internal/config"
)

// TestActionsRegistryIsFixedAllowlist is the security property this
// package's doc comment promises: the registry is a plain map literal
// enumerated in source, not built by reflecting over Agent's methods or
// any other internal/* type. A relayed action name that isn't one of
// these exact keys must never reach any internal/* call.
//
// This can't fully prove "no reflection anywhere", but it does pin the
// current registry's exact key set, so an accidental switch to a
// reflection-based or wildcard dispatcher would have to change this
// test too, not slip in silently.
func TestActionsRegistryIsFixedAllowlist(t *testing.T) {
	a := newTestAgent(t, t.TempDir()+"/settings.json", config.NewStore(t.TempDir()), "asterisk")
	got := a.actions()

	want := map[string]bool{
		"system.status":          true,
		"system.restartAsterisk": true,
		"system.reboot":          true,
		"system.dtmf":            true,
		"system.nodeStats":       true,

		"config.listNodes":               true,
		"config.loadNode":                true,
		"config.saveNode":                true,
		"config.deleteNode":              true,
		"config.setCourtesyTones":        true,
		"config.listTelemetry":           true,
		"config.setTelemetry":            true,
		"config.listFunctionMacros":      true,
		"config.saveFunctionMacro":       true,
		"config.deleteFunctionMacro":     true,
		"config.cloneNodeConfig":         true,
		"config.applyStandardCommandSet": true,
		"config.normalizeNodeConfig":     true,
		"config.recreateNodeDevice":      true,
		"config.syncExtensions":          true,

		"config.listRadioDevices":  true,
		"config.loadRadioDevice":   true,
		"config.saveRadioDevice":   true,
		"config.deleteRadioDevice": true,

		"soundSchedule.list":   true,
		"soundSchedule.save":   true,
		"soundSchedule.delete": true,

		"schedule.list":             true,
		"schedule.saveConnection":   true,
		"schedule.deleteConnection": true,

		"wxTone.list":   true,
		"wxTone.save":   true,
		"wxTone.delete": true,

		"sa818.last":    true,
		"sa818.program": true,

		"iax.loadRegistration": true,
		"iax.saveRegistration": true,

		"skywarn.listCounties":   true,
		"skywarn.getStatus":      true,
		"skywarn.setToggle":      true,
		"skywarn.setCounties":    true,
		"skywarn.addNode":        true,
		"skywarn.setPushover":    true,
		"skywarn.setSkyDescribe": true,

		"sounds.listAll": true,
		"sounds.upload":  true,
		"sounds.delete":  true,
		"sounds.preview": true,

		"rawconfig.listFiles":  true,
		"rawconfig.getFile":    true,
		"rawconfig.setKey":     true,
		"rawconfig.addKey":     true,
		"rawconfig.addSection": true,
	}
	if len(got) != len(want) {
		t.Fatalf("actions() has %d entries, want %d: %v", len(got), len(want), keysOf(got))
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("missing expected action %q", name)
		}
	}
}

func keysOf(m map[string]actionFunc) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDispatchUnknownActionIsError(t *testing.T) {
	a := newTestAgent(t, t.TempDir()+"/settings.json", config.NewStore(t.TempDir()), "asterisk")
	if _, err := a.dispatch(context.Background(), "system.reboot", nil); err == nil {
		t.Fatal("dispatch() error = nil, want an error for an action not in the registry")
	}
}

func TestDispatchSystemStatus(t *testing.T) {
	a := newTestAgent(t, t.TempDir()+"/settings.json", config.NewStore(t.TempDir()), "does-not-exist-asterisk-binary")
	result, err := a.dispatch(context.Background(), "system.status", nil)
	if err != nil {
		t.Fatalf("dispatch(system.status) error = %v", err)
	}
	if result == nil {
		t.Fatal("dispatch(system.status) returned a nil result")
	}
}
