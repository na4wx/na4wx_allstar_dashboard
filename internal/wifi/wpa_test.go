package wifi

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestWrapWpaCliErrorAddsHintForMissingCtrlInterface(t *testing.T) {
	base := errors.New("wpa_cli scan: exit status 255: Failed to connect to non-global ctrl_ifname: wlan0 error: No such file or directory")
	stderr := "Failed to connect to non-global ctrl_ifname: wlan0 error: No such file or directory\n"
	got := wrapWpaCliError([]string{"scan"}, base, stderr)
	if !strings.Contains(got.Error(), "ctrl_interface") {
		t.Errorf("wrapWpaCliError() = %q, want a hint mentioning ctrl_interface", got.Error())
	}
	if !errors.Is(got, base) {
		t.Error("wrapWpaCliError() lost the original error from errors.Is's point of view")
	}
}

func TestWrapWpaCliErrorLeavesOtherErrorsUnchanged(t *testing.T) {
	base := errors.New("wpa_cli status: exit status 1: some other failure")
	got := wrapWpaCliError([]string{"status"}, base, "some other failure\n")
	if got != base {
		t.Errorf("wrapWpaCliError() = %v, want the original error unchanged", got)
	}
}

func TestWrapWpaCliFailResultAddsHintForSaveConfig(t *testing.T) {
	got := wrapWpaCliFailResult([]string{"save_config"}, "FAIL")
	if !strings.Contains(got.Error(), "update_config") {
		t.Errorf("wrapWpaCliFailResult() = %q, want a hint mentioning update_config", got.Error())
	}
}

func TestWrapWpaCliFailResultLeavesOtherCommandsUnhinted(t *testing.T) {
	got := wrapWpaCliFailResult([]string{"select_network", "0"}, "FAIL")
	if strings.Contains(got.Error(), "update_config") {
		t.Errorf("wrapWpaCliFailResult() = %q, want no update_config hint for a non-save_config command", got.Error())
	}
}

func TestParseWpaScanResults(t *testing.T) {
	out := "bssid\tfrequency\tsignal level\tflags\tssid\n" +
		"00:11:22:33:44:55\t2412\t-45\t[WPA2-PSK-CCMP][ESS]\tMyHomeNetwork\n" +
		"aa:bb:cc:dd:ee:ff\t5180\t-70\t[WPA3-SAE-CCMP][ESS]\tSecureNet\n" +
		"11:22:33:44:55:66\t2437\t-80\t[ESS]\tOpenCafe\n" +
		// A second, weaker BSSID for the same SSID -- should not produce
		// a duplicate row, and the stronger of the two signals should win.
		"22:33:44:55:66:77\t2412\t-60\t[WPA2-PSK-CCMP][ESS]\tMyHomeNetwork\n"

	got := parseWpaScanResults(out)
	want := []Network{
		{SSID: "MyHomeNetwork", Signal: dbmToPercent(-45), Security: "WPA2"},
		{SSID: "SecureNet", Signal: dbmToPercent(-70), Security: "WPA3"},
		{SSID: "OpenCafe", Signal: dbmToPercent(-80), Security: "Open"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseWpaScanResults() = %#v, want %#v", got, want)
	}
}

func TestParseWpaScanResultsSkipsHiddenNetworks(t *testing.T) {
	out := "bssid\tfrequency\tsignal level\tflags\tssid\n" +
		"00:11:22:33:44:55\t2412\t-45\t[WPA2-PSK-CCMP][ESS]\t\n"
	got := parseWpaScanResults(out)
	if len(got) != 0 {
		t.Errorf("parseWpaScanResults() = %#v, want empty (hidden network skipped)", got)
	}
}

func TestDbmToPercentClamped(t *testing.T) {
	cases := []struct {
		dbm  int
		want int
	}{
		{-100, 0},
		{-120, 0}, // below range, clamped
		{-50, 100},
		{-40, 100}, // above range, clamped
		{-70, 60},
	}
	for _, c := range cases {
		if got := dbmToPercent(c.dbm); got != c.want {
			t.Errorf("dbmToPercent(%d) = %d, want %d", c.dbm, got, c.want)
		}
	}
}

func TestParseWpaStatusConnected(t *testing.T) {
	out := "bssid=00:11:22:33:44:55\n" +
		"freq=2412\n" +
		"ssid=MyHomeNetwork\n" +
		"id=0\n" +
		"mode=station\n" +
		"wpa_state=COMPLETED\n" +
		"ip_address=192.168.1.50\n"

	got := parseWpaStatus(out)
	want := Status{Mode: ModeClient, SSID: "MyHomeNetwork", IPAddress: "192.168.1.50"}
	if got != want {
		t.Errorf("parseWpaStatus() = %+v, want %+v", got, want)
	}
}

func TestParseWpaStatusDisconnected(t *testing.T) {
	out := "wpa_state=DISCONNECTED\n"
	got := parseWpaStatus(out)
	want := Status{Mode: ModeDisconnected}
	if got != want {
		t.Errorf("parseWpaStatus() = %+v, want %+v", got, want)
	}
}

func TestParseKnownNetworkSSIDs(t *testing.T) {
	conf := `ctrl_interface=/run/wpa_supplicant
ctrl_interface_group=0
update_config=1

network={
	ssid="Starlan IoT"
	psk=a4218bfb3c2c6a60ca9d41f364d8183eae56ba429ad3e03c9d65e4431d351c08
}

network={
	ssid="GuestWiFi"
	key_mgmt=NONE
}

network={
	ssid="Starlan IoT"
	priority=5
}
`
	got := parseKnownNetworkSSIDs(conf)
	want := []string{"Starlan IoT", "GuestWiFi"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseKnownNetworkSSIDs() = %v, want %v (duplicates must collapse, order preserved)", got, want)
	}
}

func TestParseKnownNetworkSSIDsEmpty(t *testing.T) {
	if got := parseKnownNetworkSSIDs("ctrl_interface=/run/wpa_supplicant\nupdate_config=1\n"); got != nil {
		t.Errorf("parseKnownNetworkSSIDs() = %v, want nil for a config with no network blocks", got)
	}
}

// TestParseWpaConfPathFromUnit is the direct regression test for a real
// incident: `systemctl cat` on a template unit instance prints the raw
// unit file with systemd's %I specifier still literal, so a naive
// search for "wlan0.conf" never matches at all -- confirmed on a real
// node, this exact same bug already broke install.sh's own config-file
// detection once.
func TestParseWpaConfPathFromUnit(t *testing.T) {
	unit := "[Service]\n" +
		"ExecStart=/usr/bin/wpa_supplicant -Dwext -c/etc/wpa_supplicant/wpa_supplicant_custom-%I.conf -i%I\n"
	got, err := parseWpaConfPathFromUnit(unit)
	if err != nil {
		t.Fatalf("parseWpaConfPathFromUnit() error = %v", err)
	}
	want := "/etc/wpa_supplicant/wpa_supplicant_custom-wlan0.conf"
	if got != want {
		t.Errorf("parseWpaConfPathFromUnit() = %q, want %q", got, want)
	}
}

func TestParseWpaConfPathFromUnitNoMatch(t *testing.T) {
	if _, err := parseWpaConfPathFromUnit("[Service]\nExecStart=/usr/bin/true\n"); err == nil {
		t.Error("parseWpaConfPathFromUnit() expected an error when no -c<path>.conf argument is present")
	}
}
