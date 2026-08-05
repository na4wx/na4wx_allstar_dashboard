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
		return "", wrapWpaCliError(args, fmt.Errorf("wpa_cli %s: %w: %s", strings.Join(args, " "), err, stderr.String()), stderr.String())
	}
	result := strings.TrimSpace(out.String())
	if strings.HasPrefix(result, "FAIL") {
		return "", wrapWpaCliFailResult(args, result)
	}
	return result, nil
}

// wrapWpaCliFailResult adds an actionable hint for the one plain-"FAIL"
// wpa_cli result that isn't self-explanatory: wpa_supplicant refuses
// to persist anything back to its config file unless that file itself
// sets update_config=1 -- a deliberate safety default, but one this
// HamVoIP/Arch ARM node's own config didn't have, confirmed on real
// hardware. By the time save_config runs, the network switch itself
// has already happened in memory (select_network succeeded); without
// update_config=1 it just silently wouldn't survive a reboot.
func wrapWpaCliFailResult(args []string, result string) error {
	err := fmt.Errorf("wpa_cli %s: %s", strings.Join(args, " "), result)
	if len(args) > 0 && args[0] == "save_config" {
		return fmt.Errorf(`%w (wpa_supplicant config has no "update_config=1" set, so it refuses to persist changes -- add "update_config=1" to its config file and run "systemctl restart %s", or re-run install.sh)`, err, wpaSupplicantUnit)
	}
	return err
}

// wrapWpaCliError adds an actionable hint for the one wpa_cli failure
// mode that isn't self-explanatory: wpa_supplicant only creates a
// ctrl_interface control socket if its own config file explicitly sets
// one. Confirmed missing on a real HamVoIP/Arch ARM node whose
// wpa_supplicant@wlan0.service ran against a bare "network={...}"
// block with no ctrl_interface= line at all -- install.sh now patches
// that automatically, but this covers any node it doesn't reach (a
// manually-managed config, a different distro's layout, ...).
func wrapWpaCliError(args []string, wrapped error, stderr string) error {
	if strings.Contains(stderr, "ctrl_ifname") {
		return fmt.Errorf(`%w (wpa_supplicant has no ctrl_interface configured -- add "ctrl_interface=/run/wpa_supplicant" to its config file and run "systemctl restart %s", or re-run install.sh)`, wrapped, wpaSupplicantUnit)
	}
	return wrapped
}

func (b *wpaBackend) Scan(ctx context.Context) ([]Network, error) {
	// wpa_cli talks to wpa_supplicant over a control socket that only
	// exists once wpa_supplicant is actually running on wlan0 -- without
	// this, a scan attempted before the first-ever Connect() (nothing to
	// have started the service yet) fails with "Failed to connect to
	// non-global ctrl_ifname: wlan0: No such file or directory", which
	// reads like a wpa_cli/wpa_supplicant install problem but usually
	// just means the service hasn't started yet.
	if err := ensureServiceRunning(ctx, wpaSupplicantUnit); err != nil {
		return nil, err
	}
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

// associationTimeout bounds how long Connect waits for the newly
// selected network to actually associate before treating it as
// failed and restoring every other configured network -- see
// Connect's own doc comment for why this exists. Generous for a plain
// WPA2 4-way handshake (normally a couple of seconds), which is all
// this measures -- it doesn't wait for DHCP. A var, not a const, so
// tests can shorten it rather than actually waiting out the real
// timeout on the failure path.
var associationTimeout = 15 * time.Second

// Connect adds ssid/psk as a new network and switches to it.
//
// Ordering here matters and was the direct cause of a real incident:
// the previous version called select_network (which disables every
// *other* configured network) before save_config. A save_config
// failure (e.g. wpa_supplicant's config missing update_config=1, see
// wrapWpaCliFailResult) still left the device switched off a
// previously-working network with nothing persisted to fall back to
// -- silently stranding it. Now save_config runs first, before
// anything live is touched, so a failure up to that point never
// disturbs an existing working connection. And even after
// select_network runs, if the new network doesn't actually associate
// within associationTimeout (wrong password, out of range, ...),
// every other configured network is re-enabled rather than leaving
// the operator stranded on one that doesn't work.
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

	// Removes a leftover static hotspot address before connecting, if
	// one happens to still be on the interface. Confirmed on a real
	// node: if this process restarted while the hotspot was active,
	// Manager's own in-memory hotspotActive resets to false, so a later
	// Connect() never goes through StopHotspot's own address flush --
	// leaving wlan0 with BOTH the hotspot's static 10.42.0.1 and the
	// newly-DHCP'd real address at once, which even made wpa_cli's own
	// "status" report the wrong (hotspot) IP for the real connection.
	// Best-effort and specific to just this one address, not a full
	// flush, so it can never disturb an already-good address from an
	// earlier Connect() to a different network.
	_ = runIPCmd(ctx, "addr", "del", hotspotStaticCIDR, "dev", wlan0Iface)

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
	// Persists into wpa_supplicant's own config so it survives a
	// reboot -- deliberately before enable_network/select_network below;
	// see this method's own doc comment for why the ordering matters.
	if _, err := runWpaCli(ctx, 10*time.Second, "save_config"); err != nil {
		return err
	}
	if _, err := runWpaCli(ctx, 10*time.Second, "enable_network", id); err != nil {
		return err
	}
	// Forces an immediate attempt at the new network by disabling every
	// other one. If it doesn't actually pan out, the code below
	// restores them rather than leaving this as a dead end.
	if _, err := runWpaCli(ctx, 10*time.Second, "select_network", id); err != nil {
		return err
	}
	if !b.waitForAssociation(ctx, ssid, associationTimeout) {
		_, _ = runWpaCli(ctx, 10*time.Second, "enable_network", "all")
		return fmt.Errorf("saved %q, but it did not connect within %s -- re-enabled previously configured networks so this device isn't stranded; double-check the password and try again", ssid, associationTimeout)
	}
	return nil
}

// waitForAssociation polls wpa_cli status until wlan0 is actually
// associated to ssid, or timeout elapses.
func (b *wpaBackend) waitForAssociation(ctx context.Context, ssid string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if out, err := runWpaCli(ctx, 5*time.Second, "status"); err == nil {
			if st := parseWpaStatus(out); st.Mode == ModeClient && st.SSID == ssid {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(1 * time.Second):
		}
	}
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
	// Same reasoning as Scan's own ensureServiceRunning call -- without
	// this, the System page's very first load (before wpa_supplicant has
	// ever been started) would show a WiFi status error instead of a
	// plain "not connected".
	if err := ensureServiceRunning(ctx, wpaSupplicantUnit); err != nil {
		return Status{}, err
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
