package wifi

import (
	"context"
	"net/http"
	"time"
)

// captivePortalPort is the standard, unconfigurable port every OS's
// captive-portal detection probe uses (Apple's captive.apple.com,
// Android's connectivitycheck.android.com/generate_204, Windows'
// www.msftconnecttest.com, Firefox's detectportal.firefox.com --
// deliberately plain HTTP by design on every one of these, specifically
// so a captive portal network can intercept them, even as ordinary web
// browsing has moved almost entirely to HTTPS). Redirecting every
// request on :80 to the real dashboard is the standard, widely used
// technique for triggering a phone/laptop's automatic "sign in to this
// network" prompt -- confirmed against how each OS's own probe actually
// behaves, not assumed.
const captivePortalPort = ":80"

// captivePortal is a tiny HTTP server bound to :80 while the hotspot is
// active, redirecting every request to the real dashboard. Combined
// with the wildcard DNS answer wpa_hotspot.go's dnsmasq config sets
// (every hostname resolves to this node's own IP while hotspot mode is
// active), this is what makes a phone/laptop that just joined the
// hotspot automatically pop up a sign-in prompt pointed at the
// dashboard, rather than requiring the operator to already know (or
// look up) an address to browse to.
type captivePortal struct {
	srv *http.Server
}

// startCaptivePortal starts the redirect server in the background.
// Binding :80 is best-effort: a failure here (e.g. something else
// already using it) doesn't fail hotspot startup -- the AP itself and
// manually browsing straight to the dashboard both still work fine
// without it, just without the automatic popup.
func startCaptivePortal(dashboardURL string) *captivePortal {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dashboardURL, http.StatusFound)
	})
	srv := &http.Server{Addr: captivePortalPort, Handler: mux}
	go func() {
		_ = srv.ListenAndServe() // http.ErrServerClosed on a clean Shutdown; anything else is best-effort
	}()
	return &captivePortal{srv: srv}
}

// stop shuts the redirect server down, on its own fresh timeout budget
// regardless of the caller's own context state (this is best-effort
// cleanup, not something that should inherit an already-cancelled
// context and skip straight to a force-close). Safe to call on a nil
// *captivePortal (e.g. if startCaptivePortal's bind failed) or a nil
// receiver.
func (c *captivePortal) stop() {
	if c == nil || c.srv == nil {
		return
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.srv.Shutdown(shutdownCtx); err != nil {
		_ = c.srv.Close() // best-effort force-close if graceful shutdown didn't finish in time
	}
}
