package wifi

import (
	"reflect"
	"testing"
)

// TestRedirectRuleArgs is the direct regression test for a real
// incident: HamVoIP's own stock httpd already permanently owns :80
// system-wide, so the captive-portal redirect server can never bind
// there directly. This confirms the iptables rule that instead funnels
// wlan0's own port-80 traffic to the server's real (non-80) port is
// built correctly, and that add/remove use identical match arguments --
// iptables' -D only finds a rule if its match arguments are byte-for-byte
// the same ones -A used to create it.
func TestRedirectRuleArgs(t *testing.T) {
	add := redirectRuleArgs("-A")
	want := []string{
		"-t", "nat", "-A", "PREROUTING",
		"-i", "wlan0",
		"-p", "tcp",
		"--dport", "80",
		"-j", "REDIRECT",
		"--to-port", "8090",
	}
	if !reflect.DeepEqual(add, want) {
		t.Errorf("redirectRuleArgs(\"-A\") = %v, want %v", add, want)
	}

	del := redirectRuleArgs("-D")
	if del[2] != "-D" {
		t.Errorf("redirectRuleArgs(\"-D\")[2] = %q, want \"-D\"", del[2])
	}
	del[2] = "-A"
	if !reflect.DeepEqual(del, add) {
		t.Errorf("redirectRuleArgs(\"-D\") and redirectRuleArgs(\"-A\") must share identical match arguments (only the action flag may differ), got -D=%v vs -A=%v", del, add)
	}
}
