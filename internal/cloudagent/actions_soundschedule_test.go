package cloudagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"hamvoipconfiggui/internal/config"
	"hamvoipconfiggui/internal/sounds"
	"hamvoipconfiggui/internal/soundschedule"
)

const testSoundScheduleNodeConf = "[2000]\n" +
	"rxchannel = SimpleUSB/usb\n" +
	"duplex = 1\n"

// newSoundScheduleTestAgent builds an Agent with a real config store
// (node "2000" configured, matching actions_config_test.go's own
// newConfigTestAgent fixture) and a real sounds store with exactly one
// known custom sound file -- both needed now that
// validateSoundScheduleEntry actually checks them, unlike before.
// Returns the agent and that sound's Ref (the value a real save call
// must use for File).
func newSoundScheduleTestAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	asteriskDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(asteriskDir, config.RptConfFile), []byte(testSoundScheduleNodeConf), 0644); err != nil {
		t.Fatalf("write rpt.conf fixture: %v", err)
	}
	store := config.NewStore(asteriskDir)

	soundsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(soundsDir, "test-clip.wav"), []byte("fake audio"), 0644); err != nil {
		t.Fatalf("write sound fixture: %v", err)
	}
	soundsStore := sounds.New(soundsDir, t.TempDir(), "sox")
	files, err := soundsStore.ListCustom()
	if err != nil || len(files) != 1 {
		t.Fatalf("ListCustom() = %v, %v, want exactly one fixture sound", files, err)
	}

	a := New(
		filepath.Join(t.TempDir(), "settings.json"),
		"wss://cloud.example.com/agent",
		store,
		"asterisk",
		soundsStore,
		soundschedule.New(filepath.Join(t.TempDir(), "sound-schedule.json")),
		nil, // wxTones -- unused by this action set
		"",  // skywarnDir
		"818-prog",
		filepath.Join(t.TempDir(), "sa818-last.json"),
		"", // auditLogPath
	)
	return a, files[0].Ref
}

func TestActionSoundScheduleSaveListDelete(t *testing.T) {
	a, soundRef := newSoundScheduleTestAgent(t)

	entry := soundschedule.Entry{Node: "2000", File: soundRef, Minute: "0", Hour: "*", DayOfMonth: "*", Month: "*"}
	params, _ := json.Marshal(entry)
	if _, err := a.dispatch(context.Background(), "soundSchedule.save", params); err != nil {
		t.Fatalf("save error = %v", err)
	}

	listParams, _ := json.Marshal(map[string]string{"node": "2000"})
	result, err := a.dispatch(context.Background(), "soundSchedule.list", listParams)
	if err != nil {
		t.Fatalf("list error = %v", err)
	}
	entries, ok := result.([]soundschedule.Entry)
	if !ok {
		t.Fatalf("result type = %T, want []soundschedule.Entry", result)
	}
	if len(entries) != 1 || entries[0].File != soundRef {
		t.Fatalf("entries = %+v", entries)
	}

	deleteParams, _ := json.Marshal(map[string]string{"id": entries[0].ID})
	if _, err := a.dispatch(context.Background(), "soundSchedule.delete", deleteParams); err != nil {
		t.Fatalf("delete error = %v", err)
	}
	result, err = a.dispatch(context.Background(), "soundSchedule.list", listParams)
	if err != nil {
		t.Fatalf("list after delete error = %v", err)
	}
	if entries := result.([]soundschedule.Entry); len(entries) != 0 {
		t.Fatalf("entries after delete = %+v, want empty", entries)
	}
}

func TestActionSoundScheduleSaveRejectsUnknownNode(t *testing.T) {
	a, soundRef := newSoundScheduleTestAgent(t)

	entry := soundschedule.Entry{Node: "9999", File: soundRef, Minute: "0", Hour: "*", DayOfMonth: "*", Month: "*"}
	params, _ := json.Marshal(entry)
	if _, err := a.dispatch(context.Background(), "soundSchedule.save", params); err == nil {
		t.Fatal("dispatch error = nil, want rejection of a node that isn't configured on this device")
	}
}

// TestActionSoundScheduleSaveRejectsInjectedNode specifically covers
// the vulnerability this validation closes: a node value crafted to
// inject extra content into the "rpt playback <node> <file>" string
// StartSoundSchedulePoller later builds from a saved entry.
func TestActionSoundScheduleSaveRejectsInjectedNode(t *testing.T) {
	a, soundRef := newSoundScheduleTestAgent(t)

	entry := soundschedule.Entry{Node: "2000\n!id", File: soundRef, Minute: "0", Hour: "*", DayOfMonth: "*", Month: "*"}
	params, _ := json.Marshal(entry)
	if _, err := a.dispatch(context.Background(), "soundSchedule.save", params); err == nil {
		t.Fatal("dispatch error = nil, want rejection of a node value that isn't a real, exact rpt.conf section")
	}
}

func TestActionSoundScheduleSaveRejectsUnknownFile(t *testing.T) {
	a, _ := newSoundScheduleTestAgent(t)

	entry := soundschedule.Entry{Node: "2000", File: "not-a-real-sound", Minute: "0", Hour: "*", DayOfMonth: "*", Month: "*"}
	params, _ := json.Marshal(entry)
	if _, err := a.dispatch(context.Background(), "soundSchedule.save", params); err == nil {
		t.Fatal("dispatch error = nil, want rejection of a file that isn't a known sound")
	}
}

func TestActionSoundScheduleSaveRejectsBadTimeField(t *testing.T) {
	a, soundRef := newSoundScheduleTestAgent(t)

	entry := soundschedule.Entry{Node: "2000", File: soundRef, Minute: "not-a-number", Hour: "*", DayOfMonth: "*", Month: "*"}
	params, _ := json.Marshal(entry)
	if _, err := a.dispatch(context.Background(), "soundSchedule.save", params); err == nil {
		t.Fatal("dispatch error = nil, want rejection of a non-numeric, non-'*' time field")
	}
}
