// Package server wires HTTP routes to the config and auth packages and
// renders the embedded templates.
package server

import (
	"context"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"hamvoipconfiggui/internal/auth"
	"hamvoipconfiggui/internal/cloudagent"
	"hamvoipconfiggui/internal/config"
	"hamvoipconfiggui/internal/nodedb"
	"hamvoipconfiggui/internal/sounds"
	"hamvoipconfiggui/internal/soundschedule"
	"hamvoipconfiggui/internal/system"
	"hamvoipconfiggui/internal/wifi"
	"hamvoipconfiggui/internal/wxtone"
)

const sessionCookie = "hamvoip_gui_session"

type Server struct {
	store          *config.Store
	auth           *auth.Manager
	tmpl           map[string]*template.Template
	mux            *http.ServeMux
	asteriskBin    string
	asteriskLog    string
	sa818Tool      string
	sa818StatePath string

	// history is the rolling per-node record of connection changes shown
	// on the home page, filled by StartLinkHistoryPoller (see
	// linkhistory.go). Always non-nil, so page renders work whether or
	// not the poller was started.
	history *linkHistory

	// nodes is AllStarLink's published node directory, used only to show
	// a callsign beside a node number. Always non-nil; an empty one just
	// means numbers render without callsigns.
	nodes *nodedb.Store

	// live pushes each node's moment-to-moment state to the "Right now"
	// card over SSE. Always non-nil.
	live *liveHub

	// sounds manages the operator's own custom sound files and offers
	// app_rpt's stock prompt library as read-only reference — see
	// internal/sounds's package doc for the distinction. Always non-nil.
	sounds *sounds.Store

	// soundSchedule holds the "Automation" tab's scheduled sound-playback
	// entries — see internal/soundschedule's package doc for why these
	// live outside rpt.conf, unlike scheduled connect/disconnect. Always
	// non-nil.
	soundSchedule *soundschedule.Store

	// ttsTool and ttsVoicesDir configure the "Create from text" sound
	// generator — see internal/tts's package doc. ttsTool is the piper
	// binary path, or a bare name ("piper") to resolve via PATH; a voice
	// in ttsVoicesDir is only ever selected by looking it up through
	// tts.FindVoice, never taken directly from a submitted form value.
	ttsTool      string
	ttsVoicesDir string

	// skywarnDir is where an operator-installed copy of SkywarnPlus
	// lives (installed by install.sh, not by this app at runtime — see
	// internal/skywarnplus's package doc). The "Weather Alerts" card on
	// its own SkywarnPlus tab just checks whether something's there.
	skywarnDir string

	// wxTones holds the operator's own alert-driven courtesy-tone
	// mappings — see internal/wxtone's package doc. Always non-nil.
	wxTones *wxtone.Store

	// cloudAgent is this node's optional, off-by-default connection to
	// the public cloud platform — see internal/cloudagent's package
	// doc. Always non-nil; whether it actually dials out at all is
	// controlled by cloudAgent.Settings()'s own Enabled flag, checked by
	// (*cloudagent.Agent).Run itself, not by anything here. cloudURLDefault
	// only pre-fills the Cloud Sync settings form when nothing has been
	// saved yet — see -cloud-url in main.go.
	cloudAgent      *cloudagent.Agent
	cloudURLDefault string

	// wifiManager owns wlan0's scan/connect/hotspot-fallback state --
	// see internal/wifi's package doc. Always non-nil; whether it
	// actually watches for lost connectivity at all is controlled by
	// its own enabled flag (see -wifi-hotspot-enabled in main.go), and
	// which backend (NetworkManager or wpa_supplicant/dhcpcd) it uses is
	// detected once by StartWiFiWatchdog, not here.
	wifiManager *wifi.Manager

	// restartNeeded tracks whether any Asterisk config file has been
	// saved since Asterisk was last (re)started — set via
	// config.Store.SetChangeHook (every save is to an Asterisk/app_rpt
	// file; see that hook's doc), cleared once handleSystemRestartAsterisk
	// or handleApplyRestart actually restarts it. Read by the
	// "restartNeeded" template func to show the red bar on every logged-in
	// page. Resets to false if this process restarts, independent of
	// Asterisk's own state — an acceptable gap, since that's also when an
	// operator is most likely to restart Asterisk anyway (e.g. as part of
	// a HamVoIP reboot).
	restartNeeded atomic.Bool
}

// NodeDB exposes the node directory so main can start its refresh loop.
func (s *Server) NodeDB() *nodedb.Store { return s.nodes }

// StartCloudAgent begins this node's optional, off-by-default outbound
// connection to the public cloud platform (see internal/cloudagent's
// package doc) — a supervised goroutine with its own reconnect/backoff
// lifecycle, not a time.Ticker poller like StartLinkHistoryPoller/
// StartSoundSchedulePoller/StartWXTonePoller, but started the same way:
// once, from main, for the life of the process. A no-op until the
// operator opts in on the Cloud Sync settings card.
func (s *Server) StartCloudAgent(ctx context.Context) {
	go s.cloudAgent.Run(ctx)
}

// StartWiFiWatchdog detects which network backend (NetworkManager or
// dhcpcd+wpa_supplicant) is present on this system -- one quick
// shell-out, deferred to here rather than New for the same reason as
// StartLinkHistoryPoller -- then starts wifiManager's self-healing
// hotspot-fallback loop. Call once, from main, for the life of the
// process.
func (s *Server) StartWiFiWatchdog(ctx context.Context) {
	s.wifiManager.SetBackend(wifi.DetectBackend(ctx))
	go s.wifiManager.Run(ctx)
}

// New builds a Server. templatesFS should contain web/templates and
// staticFS should contain web/static (both typically embed.FS values
// from main). asteriskBin is the path (or bare name, if it's on PATH)
// to the asterisk binary — it's not always just "asterisk" (e.g.
// HamVoIP installs it at /usr/local/hamvoip-asterisk/sbin/asterisk),
// so it's a caller-supplied value rather than hardcoded — see the
// -asterisk-bin flag in main.go. All Asterisk control (status, restart,
// the Connections page's live status/DTMF relay) goes through this
// binary's own CLI rather than systemd, since Asterisk is very often
// supervised some other way (e.g. a safe_asterisk watchdog script)
// rather than as a native systemd unit. asteriskLog is the path to
// Asterisk's full log file, shown on the System page — like
// asteriskBin, it follows wherever Asterisk is actually installed
// rather than a fixed standard location. sa818Tool is the path (or bare
// name, if on PATH) to the 818-prog SA818/DRA818 radio module
// programmer used by the System page's radio module card; sa818StatePath
// is where the last settings sent to it are recorded (see internal/sa818).
// soundsCustomDir/soundsStockDir/soxTool configure the "Tones & Audio"
// section's sound file management — see internal/sounds's package doc.
// soundSchedulePath is where the "Automation" tab's scheduled
// sound-playback entries are persisted — see internal/soundschedule's
// package doc. ttsTool/ttsVoicesDir configure the "Create from text"
// sound generator — see internal/tts's package doc. skywarnDir is where
// an operator-installed copy of SkywarnPlus lives — see
// internal/skywarnplus's package doc. wxTonesPath is where the
// operator's own alert-driven courtesy-tone mappings are persisted —
// see internal/wxtone's package doc. cloudSettingsPath is where this
// node's cloud API key/enabled flag are persisted; cloudURLDefault is
// the one cloud address this node will ever dial — fixed at
// build/deploy time (see -cloud-url in main.go), shown read-only on
// the Cloud Sync settings card and never operator-editable (see
// cloudagent.New's doc comment for why). cloudAuditLogPath is where
// every action relayed through the cloud connection is independently
// recorded — see internal/cloudagent's package doc. wifiHotspotSSID/
// wifiHotspotPSK are this node's fixed fallback-hotspot credentials
// (wifiHotspotPSK == "" broadcasts it open), wifiDashboardPort is this
// app's own -addr port (used to build the captive-portal redirect
// target, see internal/wifi's package doc), and wifiHotspotEnabled is
// the escape hatch that disables the self-healing hotspot behavior
// entirely — see internal/wifi's package doc and -wifi-hotspot-* in
// main.go.
func New(store *config.Store, authMgr *auth.Manager, templatesFS, staticFS fs.FS, asteriskBin, asteriskLog, sa818Tool, sa818StatePath, nodeDBPath, nodeDBURL, soundsCustomDir, soundsStockDir, soxTool, soundSchedulePath, ttsTool, ttsVoicesDir, skywarnDir, wxTonesPath, cloudSettingsPath, cloudURLDefault, cloudAuditLogPath, wifiHotspotSSID, wifiHotspotPSK, wifiDashboardPort string, wifiHotspotEnabled bool) (*Server, error) {
	// Built once and shared with cloudAgent below, rather than each
	// layer constructing its own -- both are thin wrappers over the same
	// on-disk state, so there's no reason for two separate instances.
	soundsStore := sounds.New(soundsCustomDir, soundsStockDir, soxTool)
	soundScheduleStore := soundschedule.New(soundSchedulePath)
	wxTonesStore := wxtone.New(wxTonesPath)

	s := &Server{store: store, auth: authMgr, mux: http.NewServeMux(), asteriskBin: asteriskBin, asteriskLog: asteriskLog, sa818Tool: sa818Tool, sa818StatePath: sa818StatePath, history: newLinkHistory(), nodes: nodedb.New(nodeDBPath, nodeDBURL), sounds: soundsStore, soundSchedule: soundScheduleStore, ttsTool: ttsTool, ttsVoicesDir: ttsVoicesDir, skywarnDir: skywarnDir, wxTones: wxTonesStore, cloudAgent: cloudagent.New(cloudSettingsPath, cloudURLDefault, store, asteriskBin, soundsStore, soundScheduleStore, wxTonesStore, skywarnDir, sa818Tool, sa818StatePath, cloudAuditLogPath), cloudURLDefault: cloudURLDefault, wifiManager: wifi.NewManager(wifiHotspotSSID, wifiHotspotPSK, wifiDashboardPort, wifiHotspotEnabled)}
	s.live = newLiveHub(s)
	store.SetChangeHook(func(string) { s.restartNeeded.Store(true) })

	tmpl, err := s.parseTemplates(templatesFS)
	if err != nil {
		return nil, err
	}
	s.tmpl = tmpl

	s.routes(staticFS)
	return s, nil
}

func (s *Server) parseTemplates(templatesFS fs.FS) (map[string]*template.Template, error) {
	pages := []string{"setup.html", "login.html", "home.html", "stats.html", "nodes_index.html", "node_new.html", "node_form.html", "config.html", "system.html", "radio_form.html"}
	funcs := template.FuncMap{"restartNeeded": func() bool { return s.restartNeeded.Load() }}
	out := map[string]*template.Template{}
	for _, page := range pages {
		// radio_device_fields.html is a shared partial ({{template
		// "radio_device_fields" ...}}), included for every page since
		// it's harmless where unused and needed by both node_form.html
		// and radio_form.html.
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templatesFS, "layout.html", "radio_device_fields.html", "node_history.html", page)
		if err != nil {
			return nil, err
		}
		out[page] = t
	}
	return out, nil
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes(staticFS fs.FS) {
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))

	s.mux.HandleFunc("GET /setup", s.handleSetupForm)
	s.mux.HandleFunc("POST /setup", s.handleSetupSubmit)
	s.mux.HandleFunc("GET /login", s.handleLoginForm)
	s.mux.HandleFunc("POST /login", s.handleLoginSubmit)
	s.mux.HandleFunc("POST /logout", s.requireAuth(s.handleLogout))

	s.mux.HandleFunc("GET /{$}", s.requireAuth(s.handleHome))
	s.mux.HandleFunc("GET /stats", s.requireAuth(s.handleStats))
	s.mux.HandleFunc("GET /api/status", s.requireAuth(s.handleAPIStatus))

	// Node pages: the one-stop shop for a node's identity, radio
	// hardware, command/tone set, AllStarLink registration, and live
	// status/DTMF/macros — everything that used to be split across the
	// Nodes, Radio, and Connections pages. GET /nodes itself is the fast
	// find-and-manage list (no live asterisk -rx calls), distinct from
	// Home (live status) and Stats (detailed history).
	s.mux.HandleFunc("GET /nodes", s.requireAuth(s.handleNodesIndex))
	s.mux.HandleFunc("GET /nodes/new", s.requireAuth(s.handleNodeNewForm))
	s.mux.HandleFunc("POST /nodes", s.requireAuth(s.handleNodeCreate))
	s.mux.HandleFunc("POST /nodes/sync-extensions", s.requireAuth(s.handleNodesSyncExtensions))
	s.mux.HandleFunc("GET /nodes/{number}", s.requireAuth(s.handleNodeEditForm))
	s.mux.HandleFunc("GET /nodes/{number}/live", s.requireAuth(s.handleNodeLive))
	s.mux.HandleFunc("POST /nodes/{number}", s.requireAuth(s.handleNodeSave))
	s.mux.HandleFunc("POST /nodes/{number}/link", s.requireAuth(s.handleNodeLink))
	s.mux.HandleFunc("POST /nodes/{number}/recreate-device", s.requireAuth(s.handleNodeRecreateDevice))
	s.mux.HandleFunc("POST /nodes/{number}/registration", s.requireAuth(s.handleNodeRegistrationSave))
	s.mux.HandleFunc("POST /nodes/{number}/clone-config", s.requireAuth(s.handleNodeCloneConfig))
	s.mux.HandleFunc("POST /nodes/{number}/normalize", s.requireAuth(s.handleNodeNormalize))
	s.mux.HandleFunc("POST /nodes/{number}/macros", s.requireAuth(s.handleNodeMacroSave))
	s.mux.HandleFunc("POST /nodes/{number}/macros/{digits}/delete", s.requireAuth(s.handleNodeMacroDelete))
	s.mux.HandleFunc("POST /nodes/{number}/macrodefs", s.requireAuth(s.handleNodeMacroDefSave))
	s.mux.HandleFunc("POST /nodes/{number}/macrodefs/{digits}/delete", s.requireAuth(s.handleNodeMacroDefDelete))
	s.mux.HandleFunc("POST /nodes/{number}/dtmf", s.requireAuth(s.handleNodeSendDTMF))
	s.mux.HandleFunc("POST /nodes/{number}/telemetry", s.requireAuth(s.handleNodeTelemetrySave))
	s.mux.HandleFunc("POST /nodes/{number}/courtesy-tones", s.requireAuth(s.handleNodeCourtesyToneSave))
	s.mux.HandleFunc("POST /nodes/{number}/sounds/upload", s.requireAuth(s.handleNodeSoundUpload))
	s.mux.HandleFunc("POST /nodes/{number}/sounds/tts", s.requireAuth(s.handleNodeSoundTTS))
	s.mux.HandleFunc("POST /nodes/{number}/sounds/tts/preview", s.requireAuth(s.handleNodeSoundTTSPreview))
	s.mux.HandleFunc("GET /nodes/{number}/sounds/{name}/audio", s.requireAuth(s.handleNodeSoundAudio))
	s.mux.HandleFunc("POST /nodes/{number}/sounds/{name}/delete", s.requireAuth(s.handleNodeSoundDelete))
	s.mux.HandleFunc("POST /nodes/{number}/automation/connections", s.requireAuth(s.handleNodeAutomationConnectionSave))
	s.mux.HandleFunc("POST /nodes/{number}/automation/connections/{macronum}/delete", s.requireAuth(s.handleNodeAutomationConnectionDelete))
	s.mux.HandleFunc("POST /nodes/{number}/automation/sounds", s.requireAuth(s.handleNodeAutomationSoundSave))
	s.mux.HandleFunc("POST /nodes/{number}/automation/sounds/{id}/delete", s.requireAuth(s.handleNodeAutomationSoundDelete))
	s.mux.HandleFunc("POST /nodes/{number}/automation/skywarn/toggle", s.requireAuth(s.handleNodeSkywarnToggle))
	s.mux.HandleFunc("POST /nodes/{number}/automation/skywarn/counties", s.requireAuth(s.handleNodeSkywarnAddCounty))
	s.mux.HandleFunc("POST /nodes/{number}/automation/skywarn/counties/{code}/delete", s.requireAuth(s.handleNodeSkywarnDeleteCounty))
	s.mux.HandleFunc("POST /nodes/{number}/automation/skywarn/register", s.requireAuth(s.handleNodeSkywarnRegister))
	s.mux.HandleFunc("POST /nodes/{number}/automation/skywarn/pushover", s.requireAuth(s.handleNodeSkywarnPushover))
	s.mux.HandleFunc("POST /nodes/{number}/automation/skywarn/skydescribe", s.requireAuth(s.handleNodeSkywarnSkyDescribe))
	s.mux.HandleFunc("POST /nodes/{number}/automation/wxtone", s.requireAuth(s.handleNodeWXToneSave))
	s.mux.HandleFunc("POST /nodes/{number}/automation/wxtone/{id}/delete", s.requireAuth(s.handleNodeWXToneDelete))
	s.mux.HandleFunc("POST /nodes/{number}/delete", s.requireAuth(s.handleNodeDelete))

	s.mux.HandleFunc("GET /config", s.requireAuth(s.handleConfigIndex))
	s.mux.HandleFunc("GET /config/{file}", s.requireAuth(s.handleConfigFile))
	s.mux.HandleFunc("POST /config/{file}", s.requireAuth(s.handleConfigSave))

	s.mux.HandleFunc("GET /system", s.requireAuth(s.handleSystemPage))
	s.mux.HandleFunc("POST /system/hostname", s.requireAuth(s.handleSystemHostname))
	s.mux.HandleFunc("POST /system/password", s.requireAuth(s.handleSystemPassword))
	s.mux.HandleFunc("POST /system/network", s.requireAuth(s.handleSystemNetwork))
	s.mux.HandleFunc("POST /system/wifi/scan", s.requireAuth(s.handleSystemWiFiScan))
	s.mux.HandleFunc("POST /system/wifi/connect", s.requireAuth(s.handleSystemWiFiConnect))
	s.mux.HandleFunc("POST /system/sharipi/apply", s.requireAuth(s.handleSystemShariApply))
	s.mux.HandleFunc("POST /system/sa818/apply", s.requireAuth(s.handleSystemSA818Apply))
	s.mux.HandleFunc("POST /system/restart-asterisk", s.requireAuth(s.handleSystemRestartAsterisk))
	s.mux.HandleFunc("POST /system/apply-restart", s.requireAuth(s.handleApplyRestart))
	s.mux.HandleFunc("POST /system/reboot", s.requireAuth(s.handleSystemReboot))
	s.mux.HandleFunc("POST /system/cloud", s.requireAuth(s.handleSystemCloudSave))
	// Radio device editing (adjusting an already-created device) — device
	// *creation* only happens inline from a node's own page now.
	s.mux.HandleFunc("GET /system/radio/{file}/{name}", s.requireAuth(s.handleRadioEditForm))
	s.mux.HandleFunc("POST /system/radio/{file}/{name}", s.requireAuth(s.handleRadioSave))
	s.mux.HandleFunc("POST /system/radio/{file}/{name}/delete", s.requireAuth(s.handleRadioDelete))
	s.mux.HandleFunc("POST /system/radio/{file}/placeholder", s.requireAuth(s.handleSystemAddPlaceholderDevice))

	// Courtesy redirects for old bookmarks — Radio/Connections no longer
	// exist as standalone pages; everything they offered is reachable
	// from System or a node's own page now.
	s.mux.HandleFunc("GET /radio", s.requireAuth(redirectTo("/system")))
	s.mux.HandleFunc("GET /connections", s.requireAuth(redirectTo("/")))
}

func redirectTo(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, path, http.StatusSeeOther)
	}
}

// pageData is the common template context. Handlers embed it and add
// page-specific fields.
type pageData struct {
	LoggedIn  bool
	FlashKind string
	Flash     string
}

func flash(kind, msg string) pageData {
	return pageData{LoggedIn: true, FlashKind: kind, Flash: msg}
}

func (s *Server) render(w http.ResponseWriter, page string, data any) {
	t, ok := s.tmpl[page]
	if !ok {
		http.Error(w, "template not found: "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("render %s: %v", page, err)
	}
}

// requireAuth wraps a handler so it 302s to /login (or /setup, if no
// account has been created yet) without a valid session cookie.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.Configured() {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if _, ok := s.auth.ValidateSession(c.Value); !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// currentUsername returns the logged-in user's name, or "" if called
// outside a requireAuth-wrapped handler (where a valid session is
// already guaranteed).
func (s *Server) currentUsername(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	username, _ := s.auth.ValidateSession(c.Value)
	return username
}

func (s *Server) handleSetupForm(w http.ResponseWriter, r *http.Request) {
	if s.auth.Configured() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, "setup.html", pageData{})
}

func (s *Server) handleSetupSubmit(w http.ResponseWriter, r *http.Request) {
	if s.auth.Configured() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm")
	if password != confirm {
		s.render(w, "setup.html", pageData{Flash: "Passwords do not match", FlashKind: "error"})
		return
	}
	if err := s.auth.SetCredentials(username, password); err != nil {
		s.render(w, "setup.html", pageData{Flash: err.Error(), FlashKind: "error"})
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if !s.auth.Configured() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	s.render(w, "login.html", pageData{})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	if !s.auth.Verify(username, password) {
		s.render(w, "login.html", pageData{Flash: "Invalid username or password", FlashKind: "error"})
		return
	}
	token, err := s.auth.CreateSession(username)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		MaxAge:   int((12 * time.Hour).Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.auth.DestroySession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	status := system.Snapshot(ctx, s.asteriskBin)
	writeJSON(w, status)
}
