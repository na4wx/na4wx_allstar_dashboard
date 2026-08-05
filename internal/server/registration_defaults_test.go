package server

import "testing"

func TestDefaultIfBlank(t *testing.T) {
	cases := []struct{ v, def, want string }{
		{"", "register.allstarlink.org", "register.allstarlink.org"},
		{"   ", "register.allstarlink.org", "register.allstarlink.org"},
		{"iax.example.org", "register.allstarlink.org", "iax.example.org"},
		{"", "friend", "friend"},
		{"peer", "friend", "peer"},
	}
	for _, c := range cases {
		if got := defaultIfBlank(c.v, c.def); got != c.want {
			t.Errorf("defaultIfBlank(%q, %q) = %q, want %q", c.v, c.def, got, c.want)
		}
	}
}

// TestDefaultNodePeer guards the values a node actually needs to be
// reachable from the AllStarLink network. A blank context or type here
// is what makes a remote node accept a connection and then immediately
// hang up, so these are asserted explicitly rather than trusted.
func TestDefaultNodePeer(t *testing.T) {
	p := defaultNodePeer("52829", "hunter2")
	if p.Node != "52829" {
		t.Errorf("Node = %q, want %q", p.Node, "52829")
	}
	if p.Secret != "hunter2" {
		t.Errorf("Secret = %q, want %q", p.Secret, "hunter2")
	}
	if p.Type != "friend" {
		t.Errorf("Type = %q, want friend", p.Type)
	}
	if p.Context != "radio-secure" {
		t.Errorf("Context = %q, want radio-secure", p.Context)
	}
	if p.Host != "dynamic" {
		t.Errorf("Host = %q, want dynamic", p.Host)
	}
	if p.Auth != "plaintext" {
		t.Errorf("Auth = %q, want plaintext", p.Auth)
	}
}
