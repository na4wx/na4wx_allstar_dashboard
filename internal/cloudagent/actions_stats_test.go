package cloudagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"hamvoipconfiggui/internal/config"
)

const fakeRptStatsOutput = `Signal on input..................................: YES
TX time today....................................: 00:01:02.3
`

// fakeStatsAsterisk writes a fake "asterisk" binary that switches on
// the "rpt <subcommand>" text in its "-rx" argument, so a single fake
// tool can answer "rpt stats", "rpt nodes", and "rpt show variables"
// differently within one test -- system.AsteriskRX always invokes it as
// `asterisk -rx "<cmd>"`, so $2 carries the whole command string.
func fakeStatsAsterisk(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "asterisk")
	script := `#!/bin/sh
case "$2" in
  "rpt stats "*) printf '%s' '` + fakeRptStatsOutput + `' ;;
  "rpt nodes "*) echo "<NONE>" ;;
  "rpt show variables "*) echo "RPT_ALINKS=0" ;;
esac
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write fake asterisk: %v", err)
	}
	return path
}

// statsTestNodeStore returns a config.Store with node "546051"
// configured -- needed now that actionSystemNodeStats validates the
// requested node against the store (see validNodeNumber's own doc
// comment) before running any Asterisk CLI commands.
func statsTestNodeStore(t *testing.T) *config.Store {
	t.Helper()
	asteriskDir := t.TempDir()
	fixture := "[546051]\n" +
		"rxchannel = SimpleUSB/usb\n" +
		"duplex = 1\n"
	if err := os.WriteFile(filepath.Join(asteriskDir, config.RptConfFile), []byte(fixture), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return config.NewStore(asteriskDir)
}

func TestActionSystemNodeStats(t *testing.T) {
	bin := fakeStatsAsterisk(t)
	a := newTestAgent(t, filepath.Join(t.TempDir(), "settings.json"), statsTestNodeStore(t), bin)

	params, _ := json.Marshal(nodeStatsParams{Number: "546051"})
	result, err := a.dispatch(context.Background(), "system.nodeStats", params)
	if err != nil {
		t.Fatalf("dispatch error = %v", err)
	}
	res, ok := result.(nodeStatsResult)
	if !ok {
		t.Fatalf("result type = %T, want nodeStatsResult", result)
	}
	if !res.StatsOK || !res.Receiving {
		t.Errorf("res = %+v, want StatsOK=true Receiving=true", res)
	}
	if len(res.Stats) != 2 || res.Stats[0].Label != "Signal on input" || res.Stats[0].Value != "YES" {
		t.Errorf("res.Stats = %+v, want the fixture's two fields", res.Stats)
	}
}

// TestActionSystemNodeStatsRejectsUnknownNode confirms the node
// validation actually blocks an unconfigured node before any Asterisk
// CLI command is built from it.
func TestActionSystemNodeStatsRejectsUnknownNode(t *testing.T) {
	bin := fakeStatsAsterisk(t)
	a := newTestAgent(t, filepath.Join(t.TempDir(), "settings.json"), statsTestNodeStore(t), bin)

	params, _ := json.Marshal(nodeStatsParams{Number: "999999"})
	if _, err := a.dispatch(context.Background(), "system.nodeStats", params); err == nil {
		t.Fatal("dispatch error = nil, want rejection of a node that isn't configured on this device")
	}
}
