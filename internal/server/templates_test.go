package server

import (
	"io/fs"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"hamvoipconfiggui/internal/auth"
	"hamvoipconfiggui/internal/config"
	"hamvoipconfiggui/internal/wifi"
	"hamvoipconfiggui/web"
)

// TestNewParsesAllTemplates is a smoke test for every embedded page
// template's Go syntax (system.html in particular, which is the one
// most likely to break silently -- html/template errors only surface
// at parse time, not at render time, and nothing else in this test
// suite ever calls New() to trigger that parse). A page with a typo'd
// {{if}}/{{range}} would otherwise only be caught by manually loading
// /system in a browser.
func TestNewParsesAllTemplates(t *testing.T) {
	newTemplateTestServer(t)
}

func newTemplateTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	store := config.NewStore(filepath.Join(dir, "asterisk-etc"))
	authMgr, err := auth.NewManager(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatalf("auth.NewManager: %v", err)
	}

	templatesFS, err := fs.Sub(web.Templates, "templates")
	if err != nil {
		t.Fatalf("fs.Sub templates: %v", err)
	}
	staticFS, err := fs.Sub(web.Static, "static")
	if err != nil {
		t.Fatalf("fs.Sub static: %v", err)
	}

	s, err := New(
		store, authMgr, templatesFS, staticFS,
		"asterisk", filepath.Join(dir, "asterisk.log"), "818-prog", filepath.Join(dir, "sa818-last.json"),
		filepath.Join(dir, "nodedb.json"), "http://example.invalid/nodedb",
		filepath.Join(dir, "sounds-custom"), filepath.Join(dir, "sounds-stock"), "sox",
		filepath.Join(dir, "sound-schedule.json"), "piper", filepath.Join(dir, "piper-voices"),
		filepath.Join(dir, "skywarnplus"), filepath.Join(dir, "wx-tones.json"),
		filepath.Join(dir, "cloud-agent.json"), "wss://cloud.example.invalid/agent", filepath.Join(dir, "cloud-actions.log"),
		"NA4WX Allstar Dashboard", "", "8088", true,
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// TestSystemPageWiFiCardRenders exercises the Wireless card's branches
// directly against real template rendering -- this dev machine has no
// real NetworkManager/wpa_supplicant to produce Connected/Hotspot
// state through the actual backend, so each Mode is faked here
// instead, the same way a live Pi's populateSystemWiFi would fill it.
func TestSystemPageWiFiCardRenders(t *testing.T) {
	s := newTemplateTestServer(t)

	cases := []struct {
		name    string
		data    systemPageData
		want    []string
		notWant []string
	}{
		{
			name: "unavailable",
			data: systemPageData{pageData: pageData{LoggedIn: true}, WiFiAvailable: false},
			want: []string{"WiFi management isn't available"},
		},
		{
			name: "client connected",
			data: systemPageData{
				pageData: pageData{LoggedIn: true}, WiFiAvailable: true, WiFiBackendName: "NetworkManager",
				WiFiStatus: wifi.Status{Mode: wifi.ModeClient, SSID: "MyHomeNetwork", IPAddress: "192.168.1.50"},
			},
			want: []string{"Connected to", "MyHomeNetwork", "192.168.1.50"},
		},
		{
			name: "hotspot active",
			data: systemPageData{
				pageData: pageData{LoggedIn: true}, WiFiAvailable: true, WiFiBackendName: "wpa_supplicant/dhcpcd", WiFiHotspotSSID: "hamvoip-gui-setup",
				WiFiStatus: wifi.Status{Mode: wifi.ModeHotspot, SSID: "hamvoip-gui-setup"},
			},
			want: []string{"Broadcasting fallback hotspot"},
		},
		{
			name: "scan results table",
			data: systemPageData{
				pageData: pageData{LoggedIn: true}, WiFiAvailable: true, WiFiBackendName: "NetworkManager",
				WiFiNetworks: []wifi.Network{{SSID: "OpenCafe", Signal: 40, Security: "Open"}, {SSID: "SecureNet", Signal: 90, Security: "WPA2"}},
			},
			want: []string{"OpenCafe", "40%", "SecureNet", "WPA2"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.render(w, "system.html", c.data)
			if w.Code != 200 {
				t.Fatalf("status = %d", w.Code)
			}
			body := w.Body.String()
			for _, want := range c.want {
				if !strings.Contains(body, want) {
					t.Errorf("body missing %q", want)
				}
			}
			for _, notWant := range c.notWant {
				if strings.Contains(body, notWant) {
					t.Errorf("body unexpectedly contains %q", notWant)
				}
			}
		})
	}
}
