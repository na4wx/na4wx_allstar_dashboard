package wifi

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Fixed paths/subnet for the wpa-backend's own hotspot -- NetworkManager
// needs none of this (see nmcli.go's StartHotspot), since it has
// built-in hotspot support. The subnet deliberately matches
// NetworkManager's own default hotspot subnet, so behavior/docs are
// identical regardless of which backend a given node ends up running.
const (
	hostapdConfPath = "/etc/hamvoip-gui/hostapd-wlan0.conf"
	dnsmasqConfPath = "/etc/hamvoip-gui/dnsmasq-wlan0.conf"
	hostapdPidPath  = "/run/hamvoip-gui/hostapd-wlan0.pid"
	dnsmasqPidPath  = "/run/hamvoip-gui/dnsmasq-wlan0.pid"

	hotspotStaticIP     = "10.42.0.1"
	hotspotStaticCIDR   = hotspotStaticIP + "/24"
	hotspotDHCPRangeLow = "10.42.0.10"
	hotspotDHCPRangeHi  = "10.42.0.200"

	// preferredHostapdPath is where install.sh installs a hostapd it
	// had to build from source, when the OS-packaged one doesn't
	// actually run (see install.sh's own hostapd-from-source section).
	// Checked explicitly rather than relying on PATH ordering to prefer
	// it automatically -- confirmed unreliable in practice on a real
	// node: the bare "hostapd" command kept resolving to the still-
	// broken packaged /usr/bin/hostapd even after a working one existed
	// at this path.
	preferredHostapdPath = "/usr/local/bin/hostapd"
)

// hostapdBinary returns preferredHostapdPath if it exists there,
// otherwise "hostapd" for a normal PATH lookup -- covers both a node
// whose packaged hostapd already works fine (no from-source build ever
// needed) and one where install.sh had to build its own.
func hostapdBinary() string {
	return hostapdBinaryAt(preferredHostapdPath)
}

// hostapdBinaryAt is hostapdBinary's own logic, parameterized for
// testability -- same "parameterized helper behind the real, fixed-path
// public function" shape as system.listNetworkInterfaces vs its own
// exported ListNetworkInterfaces wrapper.
func hostapdBinaryAt(preferredPath string) string {
	if fi, err := os.Stat(preferredPath); err == nil && !fi.IsDir() {
		return preferredPath
	}
	return "hostapd"
}

// StartHotspot broadcasts ssid on wlan0 as this node's own access
// point. psk == "" broadcasts it open (no WPA2 protection) -- same
// convention as nmcli.go's own StartHotspot.
func (b *wpaBackend) StartHotspot(ctx context.Context, ssid, psk string) error {
	if err := ValidateSSID(ssid); err != nil {
		return err
	}
	if psk != "" {
		if err := ValidatePSK(psk); err != nil {
			return err
		}
	}
	hostapdBin := hostapdBinary()
	if _, err := exec.LookPath(hostapdBin); err != nil {
		return fmt.Errorf("hostapd is not installed -- see install.sh")
	}
	if _, err := exec.LookPath("dnsmasq"); err != nil {
		return fmt.Errorf("dnsmasq is not installed -- see install.sh")
	}

	// Best-effort: hand wlan0 away from normal client-mode management
	// before reconfiguring it as an access point. Never a full "systemctl
	// restart dhcpcd" / stop of the whole dhcpcd process -- that would
	// also bounce any existing eth0 lease on a box where dhcpcd manages
	// both interfaces.
	_ = stopService(ctx, wpaSupplicantUnit)
	_ = runIP(ctx, "dhcpcd", "--release", wlan0Iface)

	succeeded := false
	defer func() {
		if !succeeded {
			_ = b.StopHotspot(ctx)
		}
	}()

	if err := runIPCmd(ctx, "addr", "flush", "dev", wlan0Iface); err != nil {
		return err
	}
	if err := runIPCmd(ctx, "addr", "add", hotspotStaticCIDR, "dev", wlan0Iface); err != nil {
		return err
	}
	if err := runIPCmd(ctx, "link", "set", wlan0Iface, "up"); err != nil {
		return err
	}

	if err := writeHostapdConf(ssid, psk); err != nil {
		return err
	}
	if err := writeDnsmasqConf(); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(hostapdPidPath), 0755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(hostapdPidPath), err)
	}
	if err := runCmd(ctx, 10*time.Second, hostapdBin, "-B", "-P", hostapdPidPath, hostapdConfPath); err != nil {
		return fmt.Errorf("start hostapd: %w", err)
	}
	if err := runCmd(ctx, 10*time.Second, "dnsmasq", "--conf-file="+dnsmasqConfPath, "--pid-file="+dnsmasqPidPath); err != nil {
		return fmt.Errorf("start dnsmasq: %w", err)
	}

	succeeded = true
	return nil
}

// StopHotspot hands wlan0 back to normal client-mode use -- called both
// for a genuine "a real connection came back" teardown and, via
// StartHotspot's own deferred cleanup, when standing up the hotspot
// failed partway through. Confirmed the hard way on real hardware: this
// used to never restart wpa_supplicant, so a StartHotspot failure left
// wlan0 with neither a hotspot nor a working client connection -- and
// since the watchdog retried every tick, it kept re-stopping
// wpa_supplicant every 30s indefinitely, a strictly worse outcome than
// doing nothing.
func (b *wpaBackend) StopHotspot(ctx context.Context) error {
	_ = stopByPidfile(dnsmasqPidPath)
	_ = stopByPidfile(hostapdPidPath)
	_ = runIPCmd(ctx, "addr", "flush", "dev", wlan0Iface)
	// Start, not restart -- idempotent if it's already running, and
	// matches ensureServiceRunning's own reasoning elsewhere in this
	// package: never bounce an already-good state unnecessarily.
	_ = startService(ctx, wpaSupplicantUnit)
	// Best-effort: hand wlan0's DHCP-client duty back to dhcpcd. Never a
	// full dhcpcd restart -- same eth0-safety reasoning as StartHotspot.
	_ = runIP(ctx, "dhcpcd", "--rebind", wlan0Iface)
	return nil
}

// writeHostapdConf writes the minimal hostapd config for an access
// point on wlan0 -- open (no security stanza at all) when psk == "",
// WPA2-PSK otherwise. 0600 since a non-empty psk embeds the passphrase.
func writeHostapdConf(ssid, psk string) error {
	if err := os.MkdirAll(filepath.Dir(hostapdConfPath), 0755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(hostapdConfPath), err)
	}
	if err := os.WriteFile(hostapdConfPath, []byte(hostapdConfContent(ssid, psk)), 0600); err != nil {
		return fmt.Errorf("write %s: %w", hostapdConfPath, err)
	}
	return nil
}

// hostapdConfContent is writeHostapdConf's own logic, parameterized for
// testability -- same shape as hostapdBinary/hostapdBinaryAt.
func hostapdConfContent(ssid, psk string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "interface=%s\n", wlan0Iface)
	b.WriteString("driver=nl80211\n")
	b.WriteString("hw_mode=g\n")
	b.WriteString("channel=6\n")
	fmt.Fprintf(&b, "ssid=%s\n", ssid)
	if psk != "" {
		b.WriteString("wpa=2\n")
		fmt.Fprintf(&b, "wpa_passphrase=%s\n", psk)
		b.WriteString("wpa_key_mgmt=WPA-PSK\n")
		b.WriteString("rsn_pairwise=CCMP\n")
	}
	return b.String()
}

// writeDnsmasqConf writes wlan0's DHCP/DNS config for the hotspot,
// including a wildcard DNS answer (see dnsmasqConfContent) so a joining
// phone/laptop's own captive-portal probe resolves straight to this
// node and triggers its OS's automatic "sign in to this network"
// prompt, rather than requiring the operator to know or look up this
// node's address.
func writeDnsmasqConf() error {
	if err := os.MkdirAll(filepath.Dir(dnsmasqConfPath), 0755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dnsmasqConfPath), err)
	}
	if err := os.WriteFile(dnsmasqConfPath, []byte(dnsmasqConfContent()), 0644); err != nil {
		return fmt.Errorf("write %s: %w", dnsmasqConfPath, err)
	}
	return nil
}

// dnsmasqConfContent is writeDnsmasqConf's own logic, parameterized for
// testability -- same shape as hostapdBinary/hostapdBinaryAt. The
// address=/#/<ip> line answers every DNS query (the "#" wildcard
// domain) with hotspotStaticIP, which is what actually makes each OS's
// captive-portal probe (e.g. Apple's captive.apple.com,
// Android's connectivitycheck.gstatic.com) resolve to this node instead
// of the real internet, so its plain-HTTP probe request lands on
// Manager's own :80 redirect (see captive_portal.go) and the OS treats
// that as a captive portal automatically.
func dnsmasqConfContent() string {
	var b strings.Builder
	fmt.Fprintf(&b, "interface=%s\n", wlan0Iface)
	b.WriteString("bind-interfaces\n")
	fmt.Fprintf(&b, "dhcp-range=%s,%s,255.255.255.0,24h\n", hotspotDHCPRangeLow, hotspotDHCPRangeHi)
	fmt.Fprintf(&b, "dhcp-option=3,%s\n", hotspotStaticIP) // router
	fmt.Fprintf(&b, "dhcp-option=6,%s\n", hotspotStaticIP) // DNS
	fmt.Fprintf(&b, "address=/#/%s\n", hotspotStaticIP)    // wildcard DNS hijack for captive-portal detection
	return b.String()
}

// wpaHotspotActive reports whether this backend's own hotspot is
// currently running, read directly from its pidfile/config rather than
// tracked in any in-memory flag -- so it's correct even right after
// this process restarts. ssid is read back from the hostapd config
// file we wrote in StartHotspot.
func wpaHotspotActive() (active bool, ssid string) {
	pid, err := readPidfile(hostapdPidPath)
	if err != nil || !pidAlive(pid) {
		return false, ""
	}
	return true, readHotspotSSIDFromConf()
}

var pidfileRe = regexp.MustCompile(`^[0-9]+$`)

// readPidfile reads and validates path's contents are digits-only
// before ever treating them as a PID -- refuses to act on anything
// that isn't an exact numeric PID, rather than attempting to sanitize
// unexpected content.
func readPidfile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(data))
	if !pidfileRe.MatchString(s) {
		return 0, fmt.Errorf("invalid pidfile contents in %s", path)
	}
	return strconv.Atoi(s)
}

func pidAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func readHotspotSSIDFromConf() string {
	data, err := os.ReadFile(hostapdConfPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "ssid="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// stopByPidfile validates path's contents are an exact numeric PID
// (see readPidfile), sends it a plain kill, and best-effort removes
// the pidfile -- never invoked with anything but our own known,
// fixed pidfile paths.
func stopByPidfile(path string) error {
	pid, err := readPidfile(path)
	if err != nil {
		return err
	}
	proc, err := os.FindProcess(pid)
	if err == nil {
		_ = proc.Signal(syscall.SIGTERM)
	}
	_ = os.Remove(path)
	return nil
}

func runIPCmd(ctx context.Context, args ...string) error {
	return runCmd(ctx, 5*time.Second, "ip", args...)
}

// runIP runs an arbitrary command with a fixed short timeout for the
// small handful of best-effort dhcpcd release/rebind calls above,
// whose own errors are always discarded by the caller.
func runIP(ctx context.Context, name string, args ...string) error {
	return runCmd(ctx, 5*time.Second, name, args...)
}

func runCmd(ctx context.Context, timeout time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stderr bytes.Buffer
	c := exec.CommandContext(ctx, name, args...)
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, stderr.String())
	}
	return nil
}
