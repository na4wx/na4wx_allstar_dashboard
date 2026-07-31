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

// wpaSupplicantUnit is the systemd template unit wpa_supplicant runs
// under on wlan0 -- the standard Raspberry Pi OS/Arch Linux ARM shape
// (one instance per interface), matching the "nohook wpa_supplicant"
// line already present in internal/netconfig's own dhcpcd.conf
// fixture (that line only disables dhcpcd's own wpa_supplicant-launch
// hook, not this systemd-managed instance).
const wpaSupplicantUnit = "wpa_supplicant@" + wlan0Iface + ".service"

type wpaBackend struct{}

func newWpaBackend() *wpaBackend { return &wpaBackend{} }

func (b *wpaBackend) Name() string { return "wpa_supplicant/dhcpcd" }

// runWpaCli runs `wpa_cli -i wlan0 <args>`. Only for non-secret
// invocations -- Connect's psk value is set through setNetworkPSK
// below instead, so no error path here ever has a psk to leak.
// wpa_cli protocol failures (e.g. an unknown network id) often exit 0
// with "FAIL" as plain stdout text rather than a non-zero process
// exit, so that's checked explicitly too.
func runWpaCli(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fullArgs := append([]string{"-i", wlan0Iface}, args...)
	var out, stderr bytes.Buffer
	c := exec.CommandContext(ctx, "wpa_cli", fullArgs...)
	c.Stdout = &out
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("wpa_cli %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	result := strings.TrimSpace(out.String())
	if strings.HasPrefix(result, "FAIL") {
		return "", fmt.Errorf("wpa_cli %s: %s", strings.Join(args, " "), result)
	}
	return result, nil
}

func (b *wpaBackend) Scan(ctx context.Context) ([]Network, error) {
	if _, err := runWpaCli(ctx, 10*time.Second, "scan"); err != nil {
		return nil, err
	}
	// scan is async in wpa_supplicant -- poll scan_results rather than a
	// single blind sleep, stopping early once at least one result comes
	// back, bounded by the caller's own ctx.
	var networks []Network
	for i := 0; i < 5; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1 * time.Second):
		}
		out, err := runWpaCli(ctx, 10*time.Second, "scan_results")
		if err != nil {
			return nil, err
		}
		networks = parseWpaScanResults(out)
		if len(networks) > 0 {
			break
		}
	}
	return networks, nil
}

// parseWpaScanResults parses `wpa_cli scan_results`'s tab-separated
// "bssid / frequency / signal level / flags / ssid" format (skipping
// its header line). Multiple BSSIDs advertising the same SSID are
// collapsed into one Network, keeping the strongest signal -- the same
// one-row-per-SSID shape nmcli's own scan naturally has.
func parseWpaScanResults(out string) []Network {
	bySSID := map[string]Network{}
	var order []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 5 || fields[0] == "bssid" {
			continue
		}
		ssid := fields[4]
		if ssid == "" {
			continue // hidden network -- nothing to offer connecting to by name
		}
		dbm, _ := strconv.Atoi(fields[2])
		n := Network{SSID: ssid, Signal: dbmToPercent(dbm), Security: normalizeWpaFlags(fields[3])}
		if existing, ok := bySSID[ssid]; !ok || n.Signal > existing.Signal {
			if !ok {
				order = append(order, ssid)
			}
			bySSID[ssid] = n
		}
	}
	networks := make([]Network, 0, len(order))
	for _, ssid := range order {
		networks = append(networks, bySSID[ssid])
	}
	return networks
}

// dbmToPercent converts a dBm signal level to a 0-100 percent using
// the common rule-of-thumb percent=2*(dBm+100), clamped -- an
// approximation so wpa_supplicant's readings are visually comparable
// to nmcli's own native 0-100 SIGNAL field, not bit-exact.
func dbmToPercent(dbm int) int {
	pct := 2 * (dbm + 100)
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

func normalizeWpaFlags(flags string) string {
	switch {
	case strings.Contains(flags, "WPA3"):
		return "WPA3"
	case strings.Contains(flags, "WPA2"):
		return "WPA2"
	case strings.Contains(flags, "WPA"):
		return "WPA"
	case strings.Contains(flags, "WEP"):
		return "WEP"
	default:
		return "Open"
	}
}

func (b *wpaBackend) Connect(ctx context.Context, ssid, psk string) error {
	if err := ValidateSSID(ssid); err != nil {
		return err
	}
	if psk != "" {
		if err := ValidatePSK(psk); err != nil {
			return err
		}
	}
	// Start, not restart -- must not drop an existing good association
	// just to configure a new one.
	if err := ensureServiceRunning(ctx, wpaSupplicantUnit); err != nil {
		return err
	}

	idOut, err := runWpaCli(ctx, 10*time.Second, "add_network")
	if err != nil {
		return err
	}
	id := strings.TrimSpace(idOut)

	if _, err := runWpaCli(ctx, 10*time.Second, "set_network", id, "ssid", `"`+ssid+`"`); err != nil {
		return err
	}
	if psk != "" {
		if err := b.setNetworkPSK(ctx, id, psk); err != nil {
			return err
		}
	} else if _, err := runWpaCli(ctx, 10*time.Second, "set_network", id, "key_mgmt", "NONE"); err != nil {
		return err
	}
	if _, err := runWpaCli(ctx, 10*time.Second, "enable_network", id); err != nil {
		return err
	}
	// Prioritizes this network over any others already configured, so
	// it's the one wpa_supplicant actually tries next.
	if _, err := runWpaCli(ctx, 10*time.Second, "select_network", id); err != nil {
		return err
	}
	// Persists into wpa_supplicant's own config so it survives a
	// reboot; dhcpcd already handles wlan0's DHCP once associated (see
	// wpaSupplicantUnit's own doc comment), so nothing further is
	// needed here.
	if _, err := runWpaCli(ctx, 10*time.Second, "save_config"); err != nil {
		return err
	}
	return nil
}

// setNetworkPSK is split out from the ordinary runWpaCli path
// specifically so the psk value never appears in any error string this
// package returns -- runWpaCli's own error wrapping includes the full
// args list, which would otherwise leak it.
func (b *wpaBackend) setNetworkPSK(ctx context.Context, id, psk string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var out, stderr bytes.Buffer
	c := exec.CommandContext(ctx, "wpa_cli", "-i", wlan0Iface, "set_network", id, "psk", `"`+psk+`"`)
	c.Stdout = &out
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("wpa_cli set_network %s psk: %w: %s", id, err, stderr.String())
	}
	if strings.HasPrefix(strings.TrimSpace(out.String()), "FAIL") {
		return fmt.Errorf("wpa_cli set_network %s psk: rejected", id)
	}
	return nil
}

func (b *wpaBackend) Status(ctx context.Context) (Status, error) {
	// Hotspot mode stops wpa_supplicant entirely (see wpa_hotspot.go),
	// so that state can't be read back from `wpa_cli status` -- check
	// it first, independent of whether wpa_supplicant is even running.
	if active, ssid := wpaHotspotActive(); active {
		return Status{Mode: ModeHotspot, SSID: ssid, IPAddress: hotspotStaticIP}, nil
	}
	out, err := runWpaCli(ctx, 5*time.Second, "status")
	if err != nil {
		return Status{}, err
	}
	return parseWpaStatus(out), nil
}

// parseWpaStatus parses `wpa_cli status`'s key=value output.
func parseWpaStatus(out string) Status {
	kv := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[key] = val
	}
	var st Status
	if kv["wpa_state"] == "COMPLETED" {
		st.Mode = ModeClient
		st.SSID = kv["ssid"]
	} else {
		st.Mode = ModeDisconnected
	}
	st.IPAddress = kv["ip_address"]
	return st
}

// ensureServiceRunning starts unit if it isn't already active --
// "start", deliberately never "restart", so an already-good connection
// is never dropped just to reconfigure something.
func ensureServiceRunning(ctx context.Context, unit string) error {
	if serviceActive(ctx, unit) {
		return nil
	}
	return startService(ctx, unit)
}

func serviceActive(ctx context.Context, unit string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out bytes.Buffer
	c := exec.CommandContext(ctx, "systemctl", "is-active", unit)
	c.Stdout = &out
	err := c.Run()
	// is-active exits non-zero for "inactive"/"unknown" -- that's a
	// valid "not active" answer, not a failure of the check itself.
	return err == nil && strings.TrimSpace(out.String()) == "active"
}

func startService(ctx context.Context, unit string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	c := exec.CommandContext(ctx, "systemctl", "start", unit)
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("systemctl start %s: %w: %s", unit, err, stderr.String())
	}
	return nil
}

func stopService(ctx context.Context, unit string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	c := exec.CommandContext(ctx, "systemctl", "stop", unit)
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("systemctl stop %s: %w: %s", unit, err, stderr.String())
	}
	return nil
}
