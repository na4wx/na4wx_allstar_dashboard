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

func TestActionSystemNodeStats(t *testing.T) {
	bin := fakeStatsAsterisk(t)
	a := newTestAgent(t, filepath.Join(t.TempDir(), "settings.json"), config.NewStore(t.TempDir()), bin)

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
