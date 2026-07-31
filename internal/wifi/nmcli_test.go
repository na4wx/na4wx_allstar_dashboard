package wifi

import (
	"reflect"
	"testing"
)

func TestSplitNmcliTerse(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []string
	}{
		{"simple", "MyNetwork:45:WPA2", []string{"MyNetwork", "45", "WPA2"}},
		{"escaped colon in ssid", `Guest\:Network:45:WPA2`, []string{"Guest:Network", "45", "WPA2"}},
		{"escaped backslash", `back\\slash:10:WPA2`, []string{`back\slash`, "10", "WPA2"}},
		{"open network trailing empty", "OpenAP::--", []string{"OpenAP", "", "--"}},
		{"empty ssid hidden network", ":50:WPA2", []string{"", "50", "WPA2"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitNmcliTerse(c.line)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("splitNmcliTerse(%q) = %#v, want %#v", c.line, got, c.want)
			}
		})
	}
}

func TestParseNmcliScan(t *testing.T) {
	out := "MyHomeNetwork:78:WPA2\n" +
		`Guest\:Network:60:WPA1 WPA2` + "\n" +
		"OpenCafe:40:--\n" +
		":30:WPA2\n" + // hidden network -- must be skipped
		"SecureNet:90:WPA3\n"

	got := parseNmcliScan(out)
	want := []Network{
		{SSID: "MyHomeNetwork", Signal: 78, Security: "WPA2"},
		{SSID: "Guest:Network", Signal: 60, Security: "WPA2"},
		{SSID: "OpenCafe", Signal: 40, Security: "Open"},
		{SSID: "SecureNet", Signal: 90, Security: "WPA3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseNmcliScan() = %#v, want %#v", got, want)
	}
}

func TestParseNmcliStatusConnected(t *testing.T) {
	out := "GENERAL.STATE:100 (connected)\n" +
		"GENERAL.CONNECTION:MyHomeNetwork\n" +
		"IP4.ADDRESS[1]:192.168.1.50/24\n"

	got := parseNmcliStatus(out)
	want := Status{Mode: ModeClient, SSID: "MyHomeNetwork", IPAddress: "192.168.1.50"}
	if got != want {
		t.Errorf("parseNmcliStatus() = %+v, want %+v", got, want)
	}
}

func TestParseNmcliStatusHotspot(t *testing.T) {
	out := "GENERAL.STATE:100 (connected)\n" +
		"GENERAL.CONNECTION:" + nmcliHotspotConnName + "\n" +
		"IP4.ADDRESS[1]:10.42.0.1/24\n"

	got := parseNmcliStatus(out)
	want := Status{Mode: ModeHotspot, SSID: nmcliHotspotConnName, IPAddress: "10.42.0.1"}
	if got != want {
		t.Errorf("parseNmcliStatus() = %+v, want %+v", got, want)
	}
}

func TestParseNmcliStatusDisconnected(t *testing.T) {
	out := "GENERAL.STATE:30 (disconnected)\n" +
		"GENERAL.CONNECTION:--\n"

	got := parseNmcliStatus(out)
	want := Status{Mode: ModeDisconnected}
	if got != want {
		t.Errorf("parseNmcliStatus() = %+v, want %+v", got, want)
	}
}
