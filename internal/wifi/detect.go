package wifi

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// DetectBackend probes the running system once and returns the Backend
// to use for every subsequent WiFi operation. Never returns nil --
// falls back to unavailableBackend{} so callers never need a nil
// check. Prefers NetworkManager when it's actively running (the
// default on newer Raspberry Pi OS/Arch Linux ARM images); otherwise
// falls back to wpa_supplicant/dhcpcd (the stack internal/netconfig's
// own static-IP handling already assumes) if both tools are present;
// otherwise WiFi management is simply unavailable on this system.
func DetectBackend(ctx context.Context) Backend {
	if networkManagerActive(ctx) {
		return newNmcliBackend()
	}
	if wpaSupplicantAvailable() {
		return newWpaBackend()
	}
	return unavailableBackend{}
}

// networkManagerActive reports whether NetworkManager's systemd unit
// is currently active. A missing unit exits non-zero with stdout
// "unknown"; a stopped one exits non-zero with "inactive" -- both must
// be treated as "not this backend", not just the exit code, since
// "systemctl is-active" can print "activating"/"deactivating" as a
// transient state a bare exit-code check would misclassify.
func networkManagerActive(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out bytes.Buffer
	c := exec.CommandContext(ctx, "systemctl", "is-active", "NetworkManager")
	c.Stdout = &out
	err := c.Run()
	return err == nil && strings.TrimSpace(out.String()) == "active"
}

// wpaSupplicantAvailable reports whether both tools this backend needs
// are on PATH. Deliberately does not also require hostapd/dnsmasq
// here -- those are only needed for StartHotspot, checked lazily
// there, so a box with wpa_supplicant but no hostapd yet still gets
// Scan/Connect/Status.
func wpaSupplicantAvailable() bool {
	_, errCli := exec.LookPath("wpa_cli")
	_, errSupplicant := exec.LookPath("wpa_supplicant")
	return errCli == nil && errSupplicant == nil
}
