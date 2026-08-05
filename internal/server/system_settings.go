package server

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"hamvoipconfiggui/internal/config"
	"hamvoipconfiggui/internal/netconfig"
	"hamvoipconfiggui/internal/sa818"
	"hamvoipconfiggui/internal/system"
	"hamvoipconfiggui/internal/wifi"
)

// These are standard paths on a stock Raspberry Pi OS image. They're
// not user-configurable from the UI since getting them wrong has no
// graceful failure mode (a bad dhcpcd.conf path means network edits
// silently go nowhere). The Asterisk log path is NOT included here —
// unlike these, it varies with where Asterisk itself is installed
// (e.g. HamVoIP's non-standard /usr/local/hamvoip-asterisk prefix), so
// it's a Server field set from -asterisk-log instead; see main.go.
const (
	dhcpcdConfPath      = "/etc/dhcpcd.conf"
	defaultNetInterface = "eth0"
)

type systemPageData struct {
	pageData
	Hostname        string
	Interfaces      []netconfig.Interface
	Static          *netconfig.StaticConfig
	DefaultIface    string
	AvailableIfaces []string
	LogLines        []string
	LogError        string
	NetError        string
	RadioDevices    []radioDeviceRef
	SA818Tool       string
	SA818Last       *sa818.LastApplied
	CTCSSOptions    []ctcssOption

	// Cloud Sync: this node's optional, off-by-default connection to the
	// public cloud platform — see internal/cloudagent's package doc and
	// populateSystemCloud. CloudAllowRemoteReboot/CloudAllowRawConfigEdit
	// are further, separately opted-in capability gates on top of
	// CloudEnabled — see cloudagent.Settings's own doc comment for why.
	CloudURL                string
	CloudAPIKey             string
	CloudEnabled            bool
	CloudAllowRemoteReboot  bool
	CloudAllowRawConfigEdit bool
	CloudLastConnected      string

	// EmptyRadioFiles lists usbradio.conf/simpleusb.conf files with zero
	// devices defined at all, regardless of whether any node actually
	// uses that driver. Found the hard way: this HamVoIP/Asterisk 1.4
	// build treats ANY loaded channel driver having no findable "active
	// device" as fatal to the whole process — even chan_usbradio failing
	// that check killed startup on a node that only uses SimpleUSB and
	// never references USBRADIO at all.
	EmptyRadioFiles []string

	// Wireless: wlan0 scan/connect and the automatic hotspot fallback —
	// see internal/wifi's package doc and populateSystemWiFi.
	// WiFiNetworks is nil except right on the response to a scan
	// (ephemeral — never persisted, unlike everything else on this
	// page).
	WiFiAvailable   bool
	WiFiBackendName string
	WiFiStatus      wifi.Status
	WiFiStatusError string
	WiFiNetworks    []wifi.Network
	WiFiHotspotSSID string
}

func (s *Server) handleSystemPage(w http.ResponseWriter, r *http.Request) {
	s.renderSystemPage(w, r, pageData{LoggedIn: true})
}

func (s *Server) renderSystemPage(w http.ResponseWriter, r *http.Request, pd pageData) {
	s.renderSystemPageWithNetworks(w, r, pd, nil)
}

// renderSystemPageWithNetworks is renderSystemPage's full body — split
// out so handleSystemWiFiScan can inject freshly scanned results into
// one specific render without a new persistence layer for what's
// inherently ephemeral data (see systemPageData.WiFiNetworks's own
// doc comment). networks is nil for every other caller.
func (s *Server) renderSystemPageWithNetworks(w http.ResponseWriter, r *http.Request, pd pageData, networks []wifi.Network) {
	ctx := r.Context()

	hostname, _ := system.Hostname(ctx)

	data := systemPageData{
		pageData:     pd,
		Hostname:     hostname,
		DefaultIface: defaultNetInterface,
	}

	if ifaces, err := netconfig.ListInterfaces(ctx); err != nil {
		data.NetError = err.Error()
	} else {
		data.Interfaces = ifaces
	}

	if static, err := netconfig.ReadManagedBlock(dhcpcdConfPath); err == nil {
		data.Static = static
	}

	if names, err := system.ListNetworkInterfaces(); err == nil {
		data.AvailableIfaces = names
	}
	// Keep the currently configured interface selectable even if it
	// isn't detected right now (e.g. this session is off-Pi, or the
	// interface is temporarily down) — otherwise saving again without
	// changing anything would silently drop it from the dropdown.
	if data.Static != nil && data.Static.Interface != "" && !slices.Contains(data.AvailableIfaces, data.Static.Interface) {
		data.AvailableIfaces = append(data.AvailableIfaces, data.Static.Interface)
	}

	if lines, err := system.LogTail(s.asteriskLog, 100); err != nil {
		data.LogError = err.Error()
	} else {
		data.LogLines = lines
	}

	data.RadioDevices = s.radioDeviceUsage()

	var emptyFiles []string
	for _, file := range radioFiles {
		if devices, err := s.store.ListRadioDevices(file); err == nil && len(devices) == 0 {
			emptyFiles = append(emptyFiles, file)
		}
	}
	data.EmptyRadioFiles = emptyFiles

	data.SA818Tool = s.sa818Tool
	if s.sa818StatePath != "" {
		if last, err := sa818.LoadLast(s.sa818StatePath); err == nil {
			data.SA818Last = last
		}
	}
	data.CTCSSOptions = ctcssOptions()

	s.populateSystemCloud(&data)
	s.populateSystemWiFi(ctx, &data)
	if networks != nil {
		data.WiFiNetworks = networks
	}

	s.render(w, "system.html", data)
}

// populateSystemWiFi fills systemPageData's Wireless fields from
// s.wifiManager. If no supported backend was detected (WiFiAvailable
// false), it stops there — WiFiStatus stays its zero value, and the
// template shows a plain "not available" line instead of the rest of
// the card.
func (s *Server) populateSystemWiFi(ctx context.Context, data *systemPageData) {
	data.WiFiHotspotSSID = s.wifiManager.HotspotSSID()
	backend := s.wifiManager.Backend()
	data.WiFiBackendName = backend.Name()
	data.WiFiAvailable = backend.Name() != "unavailable"
	if !data.WiFiAvailable {
		return
	}
	st, err := backend.Status(ctx)
	if err != nil {
		data.WiFiStatusError = err.Error()
		return
	}
	data.WiFiStatus = st
}

func (s *Server) handleSystemHostname(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("hostname"))
	pd := pageData{LoggedIn: true}
	if err := system.SetHostname(r.Context(), name); err != nil {
		pd = flash("error", err.Error())
	} else {
		pd = flash("ok", "Hostname updated. Reboot for the change to fully take effect.")
	}
	s.renderSystemPage(w, r, pd)
}

func (s *Server) handleSystemPassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username := s.currentUsername(r)
	current := r.FormValue("current_password")
	next := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	if !s.auth.Verify(username, current) {
		s.renderSystemPage(w, r, flash("error", "Current password is incorrect"))
		return
	}
	if next != confirm {
		s.renderSystemPage(w, r, flash("error", "New passwords do not match"))
		return
	}
	if err := s.auth.SetCredentials(username, next); err != nil {
		s.renderSystemPage(w, r, flash("error", err.Error()))
		return
	}
	// SetCredentials invalidates every session, including this request's;
	// send the user back through login rather than rendering a page that
	// requireAuth would immediately bounce anyway.
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleSystemNetwork(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	mode := r.FormValue("mode") // "dhcp" or "static"
	var cfg *netconfig.StaticConfig
	if mode == "static" {
		cfg = &netconfig.StaticConfig{
			Interface: strings.TrimSpace(r.FormValue("interface")),
			Address:   strings.TrimSpace(r.FormValue("address")),
			Router:    strings.TrimSpace(r.FormValue("router")),
			DNS:       strings.TrimSpace(r.FormValue("dns")),
		}
		if cfg.Interface == "" || cfg.Address == "" {
			s.renderSystemPage(w, r, flash("error", "Interface and address are required for a static configuration"))
			return
		}
	}

	if err := netconfig.WriteManagedBlock(dhcpcdConfPath, cfg); err != nil {
		s.renderSystemPage(w, r, flash("error", err.Error()))
		return
	}
	s.renderSystemPage(w, r, flash("ok", "Network configuration saved. Reboot to apply it — this does not take effect until then."))
}

// radioDeviceUsage lists every configured device across both driver
// files, annotated with which node (if any) currently references it —
// for the System page's device list, so an orphaned device (one no
// node points at) is visible and distinguishable from one in active
// use before deleting or repurposing it.
func (s *Server) radioDeviceUsage() []radioDeviceRef {
	refs := s.listAllRadioDevices()
	numbers, err := s.store.ListNodes()
	if err != nil {
		return refs
	}
	usedBy := map[radioChannelRef]string{}
	for _, num := range numbers {
		node, err := s.store.LoadNode(num)
		if err != nil {
			continue
		}
		for _, ch := range []string{node.RXChannel, node.TXChannel} {
			if ref, ok := parseRadioChannel(ch); ok {
				usedBy[ref] = num
			}
		}
	}
	for i := range refs {
		if node, ok := usedBy[radioChannelRef{File: refs[i].File, Name: refs[i].Name}]; ok {
			refs[i].UsedByNode = node
		}
	}
	return refs
}

// placeholderRadioDevice returns a generic, safe starting-point device
// config — not the operator's actual tuned audio levels, which can't be
// recovered once a stanza is gone. Shared by Home's per-node "missing
// device" fix (handleNodeRecreateDevice, dashboard.go) and the
// file-level "no device at all" fix below, so both create an
// identically-shaped placeholder.
func placeholderRadioDevice(name string) *config.RadioDevice {
	return &config.RadioDevice{
		Name:        name,
		CarrierFrom: "usb",
		TXPrelim:    "yes",
		RXMixerSet:  "500",
		TXMixerSet:  "500",
		// Off, so these placeholder levels are actually the ones used --
		// see RadioDevice.EEPROM's own doc comment.
		EEPROM: "0",
	}
}

// handleSystemAddPlaceholderDevice is the file-level counterpart to
// Home's per-node device-recreate failsafe: usbradio.conf or
// simpleusb.conf having zero devices at all is fatal to Asterisk
// startup on this HamVoIP build even when no configured node uses that
// driver (confirmed the hard way — chan_usbradio failing its "active
// device" check killed startup on a node that only ever used
// SimpleUSB). This adds a placeholder device to an otherwise-empty file
// so that driver's module load succeeds, without implying it does
// anything useful — nothing will reference it.
func (s *Server) handleSystemAddPlaceholderDevice(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	if !isRadioFileParam(file) {
		http.NotFound(w, r)
		return
	}
	if devices, err := s.store.ListRadioDevices(file); err == nil && len(devices) > 0 {
		s.renderSystemPage(w, r, flash("ok", file+" already has a device configured — nothing to do"))
		return
	}

	if err := s.store.SaveRadioDevice(file, placeholderRadioDevice("usb")); err != nil {
		s.renderSystemPage(w, r, flash("error", err.Error()))
		return
	}

	msg := "Added a placeholder device to " + file + " so its channel driver can load. Nothing actually uses it — this only exists to stop Asterisk aborting startup over an unused driver having no device."
	s.renderSystemPage(w, r, flash("ok", msg+" "+s.recheckAsteriskMessage(r.Context())))
}

// handleSystemShariApply applies the documented SHARI USB audio preset
// (see config.ApplyShariUSBPreset) to an existing radio device. It only
// touches the USB audio codec settings — SA818 frequency/tone/squelch
// programming is a separate serial-based step this doesn't cover.
func (s *Server) handleSystemShariApply(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	file, name, ok := strings.Cut(r.FormValue("device_ref"), "|")
	if !ok || !isRadioFileParam(file) || name == "" {
		s.renderSystemPage(w, r, flash("error", "Pick a radio device first"))
		return
	}

	d, err := s.store.LoadRadioDevice(file, name)
	if err != nil {
		s.renderSystemPage(w, r, flash("error", err.Error()))
		return
	}
	config.ApplyShariUSBPreset(d)
	if err := s.store.SaveRadioDevice(file, d); err != nil {
		s.renderSystemPage(w, r, flash("error", err.Error()))
		return
	}
	s.renderSystemPage(w, r, flash("ok", "Applied SHARI USB audio settings to "+name+" ("+file+"). This does not set frequency/tone on the radio module itself — that's a separate step."))
}

func (s *Server) handleSystemRestartAsterisk(w http.ResponseWriter, r *http.Request) {
	if err := system.AsteriskRestart(r.Context(), s.asteriskBin); err != nil {
		s.renderSystemPage(w, r, flash("error", err.Error()))
		return
	}
	s.restartNeeded.Store(false)
	s.renderSystemPage(w, r, flash("ok", "Asterisk restarted"))
}

// handleApplyRestart is the "Apply Changes" button on the red
// "Asterisk must be restarted" bar (see layout.html/restartNeeded),
// reachable from any page rather than just the System page. It restarts
// Asterisk the same way handleSystemRestartAsterisk does, but returns the
// operator to whatever page they clicked it from instead of always
// jumping to System — the bar disappearing is confirmation enough, so
// there's no flash message to carry across the redirect.
func (s *Server) handleApplyRestart(w http.ResponseWriter, r *http.Request) {
	if err := system.AsteriskRestart(r.Context(), s.asteriskBin); err != nil {
		s.renderSystemPage(w, r, flash("error", err.Error()))
		return
	}
	s.restartNeeded.Store(false)
	http.Redirect(w, r, refererPath(r), http.StatusSeeOther)
}

// refererPath returns the request's Referer, restricted to same-origin
// (host) so this can never be used to redirect somewhere the operator
// didn't just come from, falling back to "/" if it's missing, malformed,
// or points elsewhere.
func refererPath(r *http.Request) string {
	ref := r.Header.Get("Referer")
	if ref == "" {
		return "/"
	}
	u, err := url.Parse(ref)
	if err != nil || u.Host != r.Host || u.Path == "" {
		return "/"
	}
	if u.RawQuery != "" {
		return u.Path + "?" + u.RawQuery
	}
	return u.Path
}

func (s *Server) handleSystemReboot(w http.ResponseWriter, r *http.Request) {
	if err := system.Reboot(r.Context()); err != nil {
		s.renderSystemPage(w, r, flash("error", err.Error()))
		return
	}
	// Best-effort: the process is very likely to be killed by the
	// shutdown before this ever reaches the client.
	s.renderSystemPage(w, r, flash("ok", "Rebooting now — this page will stop responding shortly."))
}
