package server

import (
	"net/http"
	"strings"

	"hamvoipconfiggui/internal/config"
	"hamvoipconfiggui/internal/system"
)

// radioInterfaceShari is the "what is the radio hardware" answer
// meaning a SHARI / SA818-based USB node, which needs the documented
// SHARI audio preset applied at creation time — see the Interface field.
const radioInterfaceShari = "shari"

// nodeNewFormData backs the minimal "Add node" setup wizard: just
// enough fields for this app to derive and write everything else
// automatically (radio device settings, dialplan entries, IAX2 peer
// defaults, command/tone set). The full node page (node_form.html) is
// where all of that becomes visible and editable afterward — this
// page's entire job is getting there fast for someone who doesn't know
// what a dial string or a peer context is yet, and shouldn't have to.
type nodeNewFormData struct {
	pageData
	Number   string
	Callsign string
	Duplex   string

	// Interface is which radio hardware this node uses. It exists for
	// one specific, very visible failure: the generic default is
	// carrierfrom=usb, but a SHARI's carrier-detect line is inverted, so
	// with the generic value the node reads "receiving" permanently and
	// holds the transmitter keyed from the moment it's created. Asking
	// once here is the difference between a working node and one that
	// sits on the air transmitting continuously.
	Interface string

	DeviceHint string

	// DetectedDevice is set when exactly one USB sound device is
	// currently plugged in — in that case the device question is
	// skipped entirely and this is used silently. DetectedCards lists
	// what's available to pick from otherwise (zero or several
	// detected, so a guess would be wrong as often as not).
	DetectedDevice *system.SoundCard
	DetectedCards  []system.SoundCard
}

// detectRadioDevice fills DetectedDevice (skip asking) or DetectedCards
// (offer a pick list) based on what's actually plugged in right now.
func (s *Server) detectRadioDevice(data *nodeNewFormData) {
	cards, _ := system.ListSoundCards()
	if len(cards) == 1 {
		data.DetectedDevice = &cards[0]
		return
	}
	data.DetectedCards = cards
}

// defaultNodeIDRecording builds the standard station-ID announcement
// format — confirmed against a real HamVoIP node's rpt.conf earlier
// this session (|iNA4WX) — from a callsign, e.g. "n0call" -> "|iN0CALL".
func defaultNodeIDRecording(callsign string) string {
	return "|i" + strings.ToUpper(strings.TrimSpace(callsign))
}

// handleNodeNewForm defaults Interface to SHARI. The two ways to get
// this wrong are not equally bad: answering SHARI on generic hardware
// makes the node deaf (annoying, obvious, harmless), while answering
// generic on a SHARI makes it hold the transmitter down continuously,
// which jams the frequency for everyone else. Defaulting to the answer
// whose failure mode stays off the air is the safer starting point, and
// either way it's one click to change on the node's own page afterward.
func (s *Server) handleNodeNewForm(w http.ResponseWriter, r *http.Request) {
	// Duplex "2" is the real app_rpt value for a normal full-duplex
	// repeater with status tones (verified against AllStarLink's own
	// rpt.conf documentation) -- confirmed the hard way that "1" (this
	// default's own value until now) is actually half-duplex/simplex
	// with no repeated audio at all, silently handing every new node
	// created via this wizard the wrong mode unless the operator happened
	// to change this dropdown themselves. See node_form.html's own
	// duplex select for the full, corrected value/label mapping.
	data := nodeNewFormData{pageData: pageData{LoggedIn: true}, Duplex: "2", Interface: radioInterfaceShari}
	s.detectRadioDevice(&data)
	s.render(w, "node_new.html", data)
}

// handleNodeCreate is the setup wizard's submit handler. It asks for
// the minimum a human actually has to supply — node number, callsign,
// AllStarLink password, repeater mode, and (if not auto-detected) which
// radio device — and derives everything else: the radio device's own
// settings default to known-safe values (the same ones used elsewhere
// in this app), the command/tone set is cloned from an existing node or
// bootstrapped from known-good defaults with no picker shown, the
// AllStarLink peer/server settings default to the standard values this
// app's own tooltips already describe as "almost always" correct, and
// the station ID is built from the callsign. Nothing here asks for a
// dial string — confirmed earlier this session that a normal
// AllStarLink-registered node doesn't need one at all.
func (s *Server) handleNodeCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	data := nodeNewFormData{
		pageData: pageData{LoggedIn: true},
		Number:   strings.TrimSpace(r.FormValue("number")),
		Callsign: strings.TrimSpace(r.FormValue("callsign")),
		Duplex:   r.FormValue("duplex"),
		// Absent means the same thing the form itself shows selected —
		// see handleNodeNewForm for why that's SHARI. Letting a missing
		// field silently mean "generic" would diverge from what the page
		// displays, which is exactly the bug class that made the
		// registration form appear to save and write nothing.
		Interface: defaultIfBlank(r.FormValue("interface"), radioInterfaceShari),
	}
	regPassword := r.FormValue("reg_password")
	deviceName := strings.TrimSpace(r.FormValue("device_hint"))
	data.DeviceHint = deviceName
	s.detectRadioDevice(&data)
	if data.DetectedDevice != nil && deviceName == "" {
		deviceName = data.DetectedDevice.ID
	}

	fail := func(msg string) {
		data.pageData = flash("error", msg)
		s.render(w, "node_new.html", data)
	}

	if data.Number == "" {
		fail("Node number is required")
		return
	}
	if data.Callsign == "" {
		fail("Callsign is required")
		return
	}
	if regPassword == "" {
		fail("AllStarLink registration password is required")
		return
	}
	if deviceName == "" {
		fail("Pick or type a radio device name")
		return
	}

	// 1. Radio device, with safe standard defaults — saved before the
	// node itself, so a device-save failure can never leave a node
	// referencing something that doesn't exist (the exact failure mode
	// that took a real node offline during this app's development).
	// SimpleUSB is the default driver, matching this app's existing
	// "safer default for USB sound-fob interfaces" guidance. A SHARI's
	// carrier-detect line is inverted relative to that generic default,
	// so without its preset the node reads "receiving" permanently and
	// transmits continuously the moment it comes up.
	device := placeholderRadioDevice(deviceName)
	if data.Interface == radioInterfaceShari {
		config.ApplyShariUSBPreset(device)
	}
	if err := s.store.SaveRadioDevice(config.SimpleusbConfFile, device); err != nil {
		fail("Couldn't set up the radio device: " + err.Error())
		return
	}

	// 2. Node identity, with standard timing defaults filled in rather
	// than left blank for app_rpt's own unverified fallback behavior.
	n := &config.Node{
		Number:      data.Number,
		RXChannel:   radioChannelString(config.SimpleusbConfFile, deviceName),
		Duplex:      data.Duplex,
		HangTime:    "5000",
		TOTime:      "180000",
		IDTime:      "300000",
		IDRecording: defaultNodeIDRecording(data.Callsign),
	}
	if err := s.store.SaveNode(n); err != nil {
		fail(err.Error())
		return
	}

	// From here on the node itself exists, so failures are reported as
	// warnings on the full node page rather than losing the operator's
	// input — the same "best effort past the point of no return"
	// pattern the rest of this app already uses.
	warn := func(msg string) {
		s.renderNodeEditPage(w, r, n.Number, flash("error", msg))
	}

	if err := s.store.EnsureNodeExtensions(n.Number); err != nil {
		warn(extensionsSyncFailedMsg(err))
		return
	}

	// 3. Command/tone set: clone from an existing node if there is one,
	// otherwise bootstrap the standard set — either way fully automatic,
	// no picker. This is what actually makes DTMF commands work at all;
	// leaving it unset is the single most common reason a newly added
	// node silently does nothing.
	commandSetErr := func(err error) string {
		return "Node created, but setting up its command/tone set failed: " + err.Error() + " — retry from the Command/tone set section below."
	}
	if others := s.loadOtherNodeNumbers(n.Number); len(others) > 0 {
		if err := s.store.CloneNodeConfig(others[0], n.Number); err != nil {
			warn(commandSetErr(err))
			return
		}
	} else if err := s.store.ApplyStandardCommandSet(n.Number); err != nil {
		warn(commandSetErr(err))
		return
	}

	// 4. AllStarLink registration — server and peer settings default to
	// the standard values; only the password actually varies per node.
	if err := s.store.SaveRegistration(config.Registration{
		Node:     n.Number,
		Password: regPassword,
		Host:     defaultRegistrationHost,
	}); err != nil {
		warn("Node created, but AllStarLink registration failed: " + err.Error() + " — retry from the registration section below.")
		return
	}
	if err := s.store.SavePeer(defaultNodePeer(n.Number, regPassword)); err != nil {
		warn("Node created, but AllStarLink registration failed: " + err.Error() + " — retry from the registration section below.")
		return
	}

	http.Redirect(w, r, "/nodes/"+n.Number, http.StatusSeeOther)
}
