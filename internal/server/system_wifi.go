package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"hamvoipconfiggui/internal/wifi"
)

// handleSystemWiFiScan scans for nearby WiFi networks and re-renders
// the System page with the results — see systemPageData.WiFiNetworks's
// own doc comment for why these aren't persisted anywhere.
func (s *Server) handleSystemWiFiScan(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	backend := s.wifiManager.Backend()
	// Scanning needs wlan0 in station mode (the wpa_supplicant/dhcpcd
	// backend's own Scan starts wpa_supplicant on wlan0 if it isn't
	// already running) -- doing that while the fallback hotspot
	// (hostapd, AP mode) is running on the very same physical radio
	// conflicts directly with it. Confirmed on a real node: scanning
	// while joined only via the hotspot knocked the operator's own
	// device straight off it. Connect doesn't have this problem -- it's
	// *expected* to drop the hotspot as it hands wlan0 over to the new
	// network, and the operator can type the SSID/password directly
	// without ever needing a scan first.
	if st, err := backend.Status(ctx); err == nil && st.Mode == wifi.ModeHotspot {
		s.renderSystemPage(w, r, flash("error", "Can't scan while broadcasting the fallback hotspot — this node's WiFi radio is busy running it, and scanning would drop your connection to this page. Enter the network name and password directly below and click Connect instead; the hotspot will drop automatically once the new connection is confirmed."))
		return
	}

	networks, err := backend.Scan(ctx)
	if err != nil {
		s.renderSystemPage(w, r, flash("error", "Scan failed: "+err.Error()))
		return
	}
	s.renderSystemPageWithNetworks(w, r, flash("ok", fmt.Sprintf("Found %d network(s)", len(networks))), networks)
}

// handleSystemWiFiConnect connects wlan0 to the submitted network.
// password is intentionally never trimmed (a leading/trailing space is
// technically legal in a WPA passphrase, so trimming it would silently
// corrupt a real one) and never appears in any flash message, error
// string, or log — only the SSID does.
func (s *Server) handleSystemWiFiConnect(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	ssid := strings.TrimSpace(r.FormValue("ssid"))
	password := r.FormValue("password")

	if err := wifi.ValidateSSID(ssid); err != nil {
		s.renderSystemPage(w, r, flash("error", err.Error()))
		return
	}
	if password != "" {
		if err := wifi.ValidatePSK(password); err != nil {
			s.renderSystemPage(w, r, flash("error", err.Error()))
			return
		}
	}

	// Generous relative to wpa.go's own associationTimeout (15s) plus
	// overhead from several individual wpa_cli calls ahead of it.
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	// Manager.Connect (not Backend().Connect() directly) so the fallback
	// hotspot -- if it's the thing currently running on wlan0 -- gets
	// torn down properly first. See Manager.Connect's own doc comment
	// for why calling the backend directly is unsafe.
	if err := s.wifiManager.Connect(ctx, ssid, password); err != nil {
		s.renderSystemPage(w, r, flash("error", "Couldn't connect: "+err.Error()))
		return
	}
	s.renderSystemPage(w, r, flash("ok", `Connected to "`+ssid+`".`))
}
