package wifi

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// nmcliHotspotConnName is the fixed connection profile name this
// package always uses for its own fallback hotspot, so Status can tell
// "we are the ones broadcasting this" apart from any access-point
// connection the operator saved themselves.
//
// Captive-portal note: Manager's own :80 redirect (see
// captive_portal.go) works over this backend the same as the
// wpa_supplicant one -- but the wildcard DNS hijack that makes each
// OS's captive-portal probe hostname (captive.apple.com,
// connectivitycheck.android.com, ...) actually resolve to this node
// isn't wired up here, since nmcli's own "device wifi hotspot" shared-
// connection dnsmasq isn't configurable for that through the plain
// nmcli command line the way wpa_hotspot.go's own hand-written dnsmasq
// config is. A device joining an NM-backed hotspot may see a less
// reliable (or absent) automatic sign-in popup as a result; browsing
// straight to the dashboard's address still works either way.
const nmcliHotspotConnName = "hamvoip-gui-hotspot"

type nmcliBackend struct{}

func newNmcliBackend() *nmcliBackend { return &nmcliBackend{} }

func (b *nmcliBackend) Name() string { return "NetworkManager" }

// runNmcli runs nmcli with the given args and timeout, capturing
// stdout. Only for read-only/non-secret invocations (Scan, Status) --
// Connect/StartHotspot build their own error wrapping below so a psk
// never ends up embedded in a returned error string.
func runNmcli(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out, stderr bytes.Buffer
	c := exec.CommandContext(ctx, "nmcli", args...)
	c.Stdout = &out
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("nmcli %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return out.String(), nil
}

func (b *nmcliBackend) Scan(ctx context.Context) ([]Network, error) {
	out, err := runNmcli(ctx, 15*time.Second, "-t", "-f", "SSID,SIGNAL,SECURITY", "device", "wifi", "list", "--rescan", "yes")
	if err != nil {
		return nil, err
	}
	return parseNmcliScan(out), nil
}

// parseNmcliScan parses nmcli's terse (-t) SSID,SIGNAL,SECURITY
// output. nmcli's terse format backslash-escapes literal ':' and '\'
// inside field values -- a naive strings.Split(line, ":") would
// silently corrupt any SSID containing a colon (common enough, e.g.
// some router defaults), so this splits on unescaped ':' only.
func parseNmcliScan(out string) []Network {
	var networks []Network
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := splitNmcliTerse(line)
		if len(fields) < 3 {
			continue
		}
		ssid := fields[0]
		if ssid == "" {
			continue // hidden network -- nothing to offer connecting to by name
		}
		signal, _ := strconv.Atoi(fields[1])
		networks = append(networks, Network{
			SSID:     ssid,
			Signal:   signal,
			Security: normalizeNmcliSecurity(fields[2]),
		})
	}
	return networks
}

func normalizeNmcliSecurity(raw string) string {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "" || raw == "--":
		return "Open"
	case strings.Contains(raw, "WPA3"):
		return "WPA3"
	case strings.Contains(raw, "WPA2"):
		return "WPA2"
	case strings.Contains(raw, "WPA"):
		return "WPA"
	case strings.Contains(raw, "WEP"):
		return "WEP"
	default:
		return raw
	}
}

// splitNmcliTerse splits one line of nmcli -t output on unescaped ':'
// characters, unescaping '\:' -> ':' and '\\' -> '\' in each resulting
// field.
func splitNmcliTerse(line string) []string {
	var fields []string
	var cur strings.Builder
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\\' && i+1 < len(runes) && (runes[i+1] == ':' || runes[i+1] == '\\') {
			cur.WriteRune(runes[i+1])
			i++
			continue
		}
		if r == ':' {
			fields = append(fields, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	fields = append(fields, cur.String())
	return fields
}

func (b *nmcliBackend) Connect(ctx context.Context, ssid, psk string) error {
	if err := ValidateSSID(ssid); err != nil {
		return err
	}
	args := []string{"device", "wifi", "connect", ssid}
	if psk != "" {
		if err := ValidatePSK(psk); err != nil {
			return err
		}
		args = append(args, "password", psk)
	}
	args = append(args, "ifname", wlan0Iface)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	c := exec.CommandContext(ctx, "nmcli", args...)
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		// Deliberately never includes psk -- only ssid -- in the wrapped
		// error, even though it's already present in args/argv (visible
		// only to something reading this process's own /proc, not to any
		// log this app writes).
		return fmt.Errorf("nmcli device wifi connect %q: %w: %s", ssid, err, stderr.String())
	}
	return nil
}

// StartHotspot broadcasts ssid on wlan0 as this node's own access
// point. psk == "" broadcasts it open (no WPA2 protection) -- omitting
// nmcli's own "password" argument entirely is confirmed to produce a
// genuinely open network (wifi-sec.key-mgmt=none), not an
// auto-generated password.
func (b *nmcliBackend) StartHotspot(ctx context.Context, ssid, psk string) error {
	if err := ValidateSSID(ssid); err != nil {
		return err
	}
	args := []string{"device", "wifi", "hotspot", "ifname", wlan0Iface, "con-name", nmcliHotspotConnName, "ssid", ssid}
	if psk != "" {
		if err := ValidatePSK(psk); err != nil {
			return err
		}
		args = append(args, "password", psk)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	c := exec.CommandContext(ctx, "nmcli", args...)
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("nmcli device wifi hotspot %q: %w: %s", ssid, err, stderr.String())
	}
	return nil
}

func (b *nmcliBackend) StopHotspot(ctx context.Context) error {
	if _, err := runNmcli(ctx, 10*time.Second, "connection", "down", nmcliHotspotConnName); err != nil {
		return err
	}
	// Best-effort: delete the saved profile so NetworkManager can't
	// auto-reactivate a stale hotspot connection later on its own.
	_, _ = runNmcli(ctx, 10*time.Second, "connection", "delete", nmcliHotspotConnName)
	return nil
}

func (b *nmcliBackend) Status(ctx context.Context) (Status, error) {
	out, err := runNmcli(ctx, 5*time.Second, "-t", "-f", "GENERAL.STATE,GENERAL.CONNECTION,IP4.ADDRESS", "device", "show", wlan0Iface)
	if err != nil {
		return Status{}, err
	}
	return parseNmcliStatus(out), nil
}

// parseNmcliStatus parses `nmcli -t -f ... device show wlan0` output
// -- one "KEY:value" per line. Fields here are well-known values
// (state codes, connection names, IP/CIDR) that never contain a
// literal ':', so a plain first-colon split is sufficient (unlike
// Scan's freeform SSID values, which need parseNmcliScan's
// escape-aware splitter).
func parseNmcliStatus(out string) Status {
	var st Status
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch {
		case key == "GENERAL.STATE":
			if strings.HasPrefix(strings.TrimSpace(val), "100") {
				st.Mode = ModeClient
			} else {
				st.Mode = ModeDisconnected
			}
		case key == "GENERAL.CONNECTION":
			val = strings.TrimSpace(val)
			if val != "" && val != "--" {
				st.SSID = val
				if val == nmcliHotspotConnName {
					st.Mode = ModeHotspot
				}
			}
		case strings.HasPrefix(key, "IP4.ADDRESS"):
			addr, _, _ := strings.Cut(val, "/")
			if st.IPAddress == "" {
				st.IPAddress = addr
			}
		}
	}
	return st
}
