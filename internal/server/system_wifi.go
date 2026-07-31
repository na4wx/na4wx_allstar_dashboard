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

	networks, err := s.wifiManager.Backend().Scan(ctx)
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

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.wifiManager.Backend().Connect(ctx, ssid, password); err != nil {
		s.renderSystemPage(w, r, flash("error", "Couldn't connect: "+err.Error()))
		return
	}
	s.wifiManager.NotifyConnectAttempt()
	s.renderSystemPage(w, r, flash("ok", `Sent connect request to "`+ssid+`". If this device was showing its fallback hotspot, that will drop automatically once the new connection is confirmed.`))
}
