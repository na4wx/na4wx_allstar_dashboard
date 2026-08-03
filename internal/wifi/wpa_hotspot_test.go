package wifi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHostapdBinaryAt is the direct regression test for a real
// incident: on a node where install.sh had to build hostapd from
// source (because the packaged one was broken), the bare "hostapd"
// command still resolved to the old broken binary via PATH ordering,
// even after a working one existed at preferredHostapdPath.
func TestHostapdBinaryAt(t *testing.T) {
	t.Run("prefers the built binary when present", func(t *testing.T) {
		dir := t.TempDir()
		built := filepath.Join(dir, "hostapd")
		if err := os.WriteFile(built, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatalf("write fake built hostapd: %v", err)
		}
		if got := hostapdBinaryAt(built); got != built {
			t.Errorf("hostapdBinaryAt() = %q, want %q", got, built)
		}
	})

	t.Run("falls back to PATH lookup when not present", func(t *testing.T) {
		nonexistent := filepath.Join(t.TempDir(), "hostapd")
		if got := hostapdBinaryAt(nonexistent); got != "hostapd" {
			t.Errorf("hostapdBinaryAt() = %q, want \"hostapd\"", got)
		}
	})

	t.Run("falls back when the path is a directory, not a file", func(t *testing.T) {
		dir := t.TempDir()
		if got := hostapdBinaryAt(dir); got != "hostapd" {
			t.Errorf("hostapdBinaryAt() = %q, want \"hostapd\"", got)
		}
	})
}

// TestHostapdConfContent covers the SSID/password/captive-portal
// feature request: an empty psk must produce a genuinely open network
// (no wpa/wpa_passphrase/wpa_key_mgmt/rsn_pairwise lines at all), not
// merely an empty passphrase, since hostapd treats a present-but-empty
// wpa_passphrase as a config error rather than "no security".
func TestHostapdConfContent(t *testing.T) {
	t.Run("non-empty psk produces a WPA2-PSK config", func(t *testing.T) {
		got := hostapdConfContent("NA4WX Allstar Dashboard", "supersecret")
		for _, want := range []string{"ssid=NA4WX Allstar Dashboard\n", "wpa=2\n", "wpa_passphrase=supersecret\n", "wpa_key_mgmt=WPA-PSK\n", "rsn_pairwise=CCMP\n"} {
			if !strings.Contains(got, want) {
				t.Errorf("hostapdConfContent() missing %q, got:\n%s", want, got)
			}
		}
	})

	t.Run("empty psk produces an open network with no security lines", func(t *testing.T) {
		got := hostapdConfContent("NA4WX Allstar Dashboard", "")
		if !strings.Contains(got, "ssid=NA4WX Allstar Dashboard\n") {
			t.Errorf("hostapdConfContent() missing ssid line, got:\n%s", got)
		}
		for _, unwanted := range []string{"wpa=", "wpa_passphrase=", "wpa_key_mgmt=", "rsn_pairwise="} {
			if strings.Contains(got, unwanted) {
				t.Errorf("hostapdConfContent() with empty psk should omit %q, got:\n%s", unwanted, got)
			}
		}
	})
}

// TestDnsmasqConfContent is the direct regression/coverage test for the
// captive-portal feature request: without the wildcard address=/#/ DNS
// line, a joining phone/laptop's own OS captive-portal probe resolves
// out to the real internet instead of this node, and the automatic
// "sign in to this network" prompt never appears.
func TestDnsmasqConfContent(t *testing.T) {
	got := dnsmasqConfContent()
	want := "address=/#/" + hotspotStaticIP + "\n"
	if !strings.Contains(got, want) {
		t.Errorf("dnsmasqConfContent() missing wildcard DNS hijack line %q, got:\n%s", want, got)
	}
}

// TestDnsmasqConfContentExcludesLoopback is the direct regression test
// for a real incident: dnsmasq's own documented behavior silently adds
// the loopback interface to its listen set whenever interface= is
// used, which collided with named/BIND's own legitimate 127.0.0.1:53
// listener and made the hotspot's dnsmasq fail to start entirely.
func TestDnsmasqConfContentExcludesLoopback(t *testing.T) {
	got := dnsmasqConfContent()
	if !strings.Contains(got, "except-interface=lo\n") {
		t.Errorf("dnsmasqConfContent() missing except-interface=lo, got:\n%s", got)
	}
}
