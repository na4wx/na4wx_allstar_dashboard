package wifi

import (
	"reflect"
	"testing"
)

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
