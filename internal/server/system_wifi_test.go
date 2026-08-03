package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"hamvoipconfiggui/internal/wifi"
)

// fakeWiFiBackend is a minimal wifi.Backend for exercising
// handleSystemWiFiScan's own logic directly, without any real
// NetworkManager/wpa_supplicant on the machine running the test suite.
type fakeWiFiBackend struct {
	mode       wifi.Mode
	scanCalls  int
	scanResult []wifi.Network
}

func (f *fakeWiFiBackend) Name() string { return "fake" }
func (f *fakeWiFiBackend) Scan(context.Context) ([]wifi.Network, error) {
	f.scanCalls++
	return f.scanResult, nil
}
func (f *fakeWiFiBackend) Connect(context.Context, string, string) error { return nil }
func (f *fakeWiFiBackend) Status(context.Context) (wifi.Status, error) {
	return wifi.Status{Mode: f.mode}, nil
}
func (f *fakeWiFiBackend) StartHotspot(context.Context, string, string) error { return nil }
func (f *fakeWiFiBackend) StopHotspot(context.Context) error                  { return nil }

// TestHandleSystemWiFiScanRefusesWhileHotspotActive is the direct
// regression test for a real incident: the wpa_supplicant/dhcpcd
// backend's own Scan starts wpa_supplicant on wlan0, which conflicts
// directly with hostapd running AP mode on that same physical radio --
// confirmed on a real node, scanning while joined only via the fallback
// hotspot knocked the operator's own device straight off it.
func TestHandleSystemWiFiScanRefusesWhileHotspotActive(t *testing.T) {
	s := newTemplateTestServer(t)
	fb := &fakeWiFiBackend{mode: wifi.ModeHotspot}
	s.wifiManager.SetBackend(fb)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "http://example.com/system/wifi/scan", nil)
	s.handleSystemWiFiScan(w, r)

	if fb.scanCalls != 0 {
		t.Errorf("Scan calls = %d, want 0 while the hotspot is active", fb.scanCalls)
	}
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Can&#39;t scan while broadcasting") && !strings.Contains(w.Body.String(), "Can't scan while broadcasting") {
		t.Errorf("body missing the explanatory error, got:\n%s", w.Body.String())
	}
}

// TestHandleSystemWiFiScanAllowedWhenNotHotspot makes sure the guard
// above is conditional, not blanket -- scanning must still work
// normally any time the hotspot isn't the thing running on wlan0.
func TestHandleSystemWiFiScanAllowedWhenNotHotspot(t *testing.T) {
	s := newTemplateTestServer(t)
	fb := &fakeWiFiBackend{mode: wifi.ModeDisconnected, scanResult: []wifi.Network{{SSID: "TestNet", Signal: 50, Security: "WPA2"}}}
	s.wifiManager.SetBackend(fb)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "http://example.com/system/wifi/scan", nil)
	s.handleSystemWiFiScan(w, r)

	if fb.scanCalls != 1 {
		t.Errorf("Scan calls = %d, want 1", fb.scanCalls)
	}
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "TestNet") {
		t.Errorf("body missing scanned network, got:\n%s", w.Body.String())
	}
}
