package wifi

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// defaultRouteExists reports whether the kernel currently has any
// default route -- the semantically correct definition of "is there an
// active network connection" for Manager's watchdog. Checking "does
// some interface merely have an IP" instead would be wrong: wlan0 in
// hotspot mode has a (self-assigned) IP too, which would make the
// watchdog think it was already connected and never actually recover.
//
// excludeIface, when non-empty, ignores any default route through that
// device -- checkAndAct passes wlan0 whenever the fallback hotspot is
// currently active. Confirmed the hard way on a real node: dhcpcd kept
// watching wlan0's carrier state even after StartHotspot's own "dhcpcd
// --release", and ended up negotiating the node itself a DHCP lease
// straight from the hotspot's own dnsmasq server -- installing a
// default route back out through wlan0 and fooling the watchdog into
// tearing the hotspot down again within one tick of it coming up, right
// as a phone was joining. A default route via wlan0 is only trustworthy
// when the hotspot isn't the thing running on wlan0 in the first place
// (e.g. a real client-mode WiFi connection) -- so this exclusion is
// never unconditional, only applied while hotspotActive.
func defaultRouteExists(ctx context.Context, excludeIface string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out, stderr bytes.Buffer
	c := exec.CommandContext(ctx, "ip", "route", "show", "default")
	c.Stdout = &out
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return false, err
	}
	return parseDefaultRouteOutput(out.String(), excludeIface), nil
}

// parseDefaultRouteOutput is defaultRouteExists's own logic,
// parameterized for testability -- same shape as hostapdBinary/
// hostapdBinaryAt.
func parseDefaultRouteOutput(out, excludeIface string) bool {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if excludeIface != "" && strings.Contains(line, "dev "+excludeIface) {
			continue
		}
		return true
	}
	return false
}
