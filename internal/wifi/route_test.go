package wifi

import "testing"

// TestParseDefaultRouteOutput is the direct regression test for a real
// incident: the fallback hotspot came up, a phone associated and got a
// DHCP lease, and then the very next watchdog tick tore the hotspot
// back down -- traced to dhcpcd negotiating the node itself a lease
// from its own hotspot's dnsmasq server on wlan0, installing a default
// route back out through wlan0 that fooled checkAndAct into thinking a
// real connection had come back.
func TestParseDefaultRouteOutput(t *testing.T) {
	t.Run("a route through the excluded interface alone doesn't count", func(t *testing.T) {
		out := "default via 10.42.0.1 dev wlan0 proto dhcp metric 600\n"
		if parseDefaultRouteOutput(out, "wlan0") {
			t.Error("parseDefaultRouteOutput() = true, want false for a route only through the excluded interface")
		}
	})

	t.Run("a route through a different interface still counts", func(t *testing.T) {
		out := "default via 10.0.0.1 dev eth0 proto dhcp metric 100\n"
		if !parseDefaultRouteOutput(out, "wlan0") {
			t.Error("parseDefaultRouteOutput() = false, want true for a route through a non-excluded interface")
		}
	})

	t.Run("a route through the excluded interface is honored when nothing is excluded", func(t *testing.T) {
		out := "default via 10.42.0.1 dev wlan0 proto dhcp metric 600\n"
		if !parseDefaultRouteOutput(out, "") {
			t.Error("parseDefaultRouteOutput() = false, want true when excludeIface is empty -- a real client-mode wlan0 connection must still count")
		}
	})

	t.Run("no routes at all", func(t *testing.T) {
		if parseDefaultRouteOutput("", "wlan0") {
			t.Error("parseDefaultRouteOutput() = true, want false for empty output")
		}
	})

	t.Run("multiple routes -- one through a real interface takes precedence", func(t *testing.T) {
		out := "default via 10.0.0.1 dev eth0 proto dhcp metric 100\ndefault via 10.42.0.1 dev wlan0 proto dhcp metric 600\n"
		if !parseDefaultRouteOutput(out, "wlan0") {
			t.Error("parseDefaultRouteOutput() = false, want true -- a genuine eth0 route should still count even alongside an excluded wlan0 one")
		}
	})
}
