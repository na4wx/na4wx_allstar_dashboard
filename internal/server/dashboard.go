package server

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"hamvoipconfiggui/internal/config"
	"hamvoipconfiggui/internal/rptstatus"
	"hamvoipconfiggui/internal/system"
)

// radioChannelRef is a parsed rpt.conf rxchannel/txchannel value like
// "SimpleUSB/usb" — the driver prefix maps directly to which config
// file the device is expected to live in.
type radioChannelRef struct {
	File string
	Name string
}

// parseRadioChannel splits a channel string into the file/name a radio
// device is stored under, or ok=false if it's not a driver this app
// manages (e.g. a Voter or other non-USB channel type, which this
// health check has no basis to say anything about).
func parseRadioChannel(channel string) (ref radioChannelRef, ok bool) {
	driver, name, found := strings.Cut(channel, "/")
	if !found || name == "" {
		return radioChannelRef{}, false
	}
	switch driver {
	case "USBRADIO":
		return radioChannelRef{File: config.UsbradioConfFile, Name: name}, true
	case "SimpleUSB":
		return radioChannelRef{File: config.SimpleusbConfFile, Name: name}, true
	default:
		return radioChannelRef{}, false
	}
}

// radioChannelString is parseRadioChannel's inverse — builds the exact
// rpt.conf rxchannel/txchannel string a device's file+name should be
// referenced as.
func radioChannelString(file, name string) string {
	switch file {
	case config.UsbradioConfFile:
		return "USBRADIO/" + name
	case config.SimpleusbConfFile:
		return "SimpleUSB/" + name
	}
	return name
}

// nodeQuickStatus is one node's at-a-glance info on Home: who else is
// connected, app_rpt's own link-activity output, and whether its
// configured radio device actually exists. The activity output is
// shown unparsed rather than reformatted into a "currently
// transmitting: yes/no" claim — rpt lstats's exact columns vary by
// app_rpt version, and this app has no hardware to verify a parsed
// interpretation against.
type nodeQuickStatus struct {
	Node         *config.Node
	Connected    string
	ConnectedErr string
	Activity     string
	ActivityErr  string

	// MissingDevice is set when this node's rxchannel points at a
	// device that doesn't exist in usbradio.conf/simpleusb.conf — the
	// exact condition that makes chan_simpleusb/chan_usbradio refuse to
	// load and Asterisk fail to start (found the hard way: a node's
	// device stanza had vanished, apparently from something outside
	// this app regenerating the file, e.g. HamVoIP's node-config.sh).
	MissingDevice *radioChannelRef

	// The two history tables shown for this node, newest first — see
	// rptstatus.BuildLinkTables. ActivityHeaders is taken from app_rpt's
	// own output rather than named here, so a different app_rpt
	// version's columns still render correctly.
	ConnectedHistory []rptstatus.ConnectedRecord
	ActivityHeaders  []string
	ActivityHistory  []rptstatus.ActivityRecord

	// Live state from "rpt stats". Receiving means someone is keying
	// this node's receiver right now; see rptstatus.NodeReceiving for
	// what that does and doesn't cover. StatsRaw is shown instead of the
	// table when the output didn't parse.
	Stats     rptstatus.StatFields
	StatsOK   bool
	StatsRaw  string
	StatsErr  string
	Receiving bool

	// NowConnected is the current connected-node list with callsigns —
	// the same data as the newest history row, surfaced separately so
	// the live card doesn't make the reader parse a table to answer
	// "who is on right now".
	NowConnected []rptstatus.ConnectedNode
}

type homePageData struct {
	pageData
	Nodes  []*config.Node
	Status system.Status
	Quick  []nodeQuickStatus

	// OwnerSubscriptionActive mirrors cloudagent.Agent's own field of
	// the same name -- only populated for home.html (see renderHome),
	// which swaps its "manage from anywhere" promo card for a
	// thank-you once this node's cloud account has an active paid
	// subscription. Left at its false zero value for stats.html, which
	// doesn't read it.
	OwnerSubscriptionActive bool
}

// handleHome is the sole landing page: one card per configured node with
// live on-air/connected status, quick link/unlink, and (absorbed from
// what used to be a separate Nodes list page) links into each node's
// full configuration. Detailed stats, connection history, and overall
// system status live on the separate Stats page.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	s.renderHome(w, r, pageData{LoggedIn: true})
}

func (s *Server) renderHome(w http.ResponseWriter, r *http.Request, pd pageData) {
	nodes, status, quick, err := s.gatherNodeStatuses(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "home.html", homePageData{
		pageData:                pd,
		Nodes:                   nodes,
		Status:                  status,
		Quick:                   quick,
		OwnerSubscriptionActive: s.cloudAgent.OwnerSubscriptionActive(),
	})
}

// handleStats is the detailed-status page: overall system status plus,
// per node, the full rpt-stats field table and connection history —
// everything Home used to show below its "Right now" card before that
// content moved here to keep Home to just the live glance and the
// link/unlink action.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	s.renderStats(w, r, pageData{LoggedIn: true})
}

func (s *Server) renderStats(w http.ResponseWriter, r *http.Request, pd pageData) {
	nodes, status, quick, err := s.gatherNodeStatuses(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "stats.html", homePageData{
		pageData: pd,
		Nodes:    nodes,
		Status:   status,
		Quick:    quick,
	})
}

// gatherNodeStatuses reads everything Home and Stats both need: every
// configured node, overall system status, and each node's quick status
// (connected nodes, link activity, live stats, and connection history).
// Shared so the two pages can't drift on what a "current reading" means,
// and so this — the most CLI-call-heavy read in the app — is written
// once.
func (s *Server) gatherNodeStatuses(r *http.Request) ([]*config.Node, system.Status, []nodeQuickStatus, error) {
	numbers, err := s.store.ListNodes()
	if err != nil {
		return nil, system.Status{}, nil, err
	}
	var nodes []*config.Node
	for _, n := range numbers {
		node, err := s.store.LoadNode(n)
		if err != nil {
			continue // skip malformed entries rather than failing the whole page
		}
		nodes = append(nodes, node)
	}

	status := system.Snapshot(r.Context(), s.asteriskBin)

	var quick []nodeQuickStatus
	for _, node := range nodes {
		q := nodeQuickStatus{Node: node}
		if out, err := system.AsteriskRX(r.Context(), s.asteriskBin, "rpt nodes "+node.Number); err != nil {
			q.ConnectedErr = err.Error()
		} else {
			q.Connected = out
		}
		if out, err := system.AsteriskRX(r.Context(), s.asteriskBin, "rpt lstats "+node.Number); err != nil {
			q.ActivityErr = err.Error()
		} else {
			q.Activity = out
		}
		if ref, ok := parseRadioChannel(node.RXChannel); ok {
			if devices, err := s.store.ListRadioDevices(ref.File); err == nil && !slices.Contains(devices, ref.Name) {
				q.MissingDevice = &ref
			}
		}
		// Fold this render's reading into the history too, so a change
		// that happened between polls still gets recorded rather than
		// waiting for the next tick.
		if q.ConnectedErr == "" {
			s.history.record(node.Number, q.Connected, q.Activity)
		}
		q.ConnectedHistory, q.ActivityHeaders, q.ActivityHistory = rptstatus.BuildLinkTables(s.nodes, s.history.forNode(node.Number))

		// Live state, for Home's "Right now" card and the Stats page's
		// detailed field table.
		if out, err := system.AsteriskRX(r.Context(), s.asteriskBin, "rpt stats "+node.Number); err != nil {
			q.StatsErr = err.Error()
		} else {
			q.StatsRaw = out
			q.Stats, q.StatsOK = rptstatus.ParseRptStats(out)
			q.Receiving = rptstatus.NodeReceiving(q.Stats)
		}
		for _, number := range rptstatus.ParseConnectedNodes(q.Connected) {
			q.NowConnected = append(q.NowConnected, rptstatus.DescribeNode(s.nodes, number))
		}
		// Mark which connected nodes are keying right now (RPT_ALINKS) —
		// the same live read the SSE stream uses, so the page-load
		// snapshot and the first pushed update agree.
		s.markKeyed(r.Context(), node.Number, q.NowConnected)

		quick = append(quick, q)
	}

	return nodes, status, quick, nil
}

// handleNodesSyncExtensions is the bulk counterpart to the per-node
// EnsureNodeExtensions call in handleNodeCreate/handleNodeSave: it
// backfills every configured node's extensions.conf dialplan entries in
// one pass, for nodes that already existed (or lost their entries the
// same way rpt.conf's own did) before this app started managing that
// file, without requiring an individual visit-and-resave per node.
// EnsureNodeExtensions only ever adds missing entries, never touches
// existing ones, so this is safe to run repeatedly.
func (s *Server) handleNodesSyncExtensions(w http.ResponseWriter, r *http.Request) {
	numbers, err := s.store.ListNodes()
	if err != nil {
		s.renderHome(w, r, flash("error", err.Error()))
		return
	}

	var failed []string
	for _, number := range numbers {
		if err := s.store.EnsureNodeExtensions(number); err != nil {
			failed = append(failed, number+": "+err.Error())
		}
	}

	ok := len(numbers) - len(failed)
	msg := "Synced dialplan entries for " + strconv.Itoa(ok) + " of " + strconv.Itoa(len(numbers)) + " node(s)."
	if len(failed) > 0 {
		s.renderHome(w, r, flash("error", msg+" Failed: "+strings.Join(failed, "; ")))
		return
	}
	s.renderHome(w, r, flash("ok", msg))
}

// handleNodeLink sends a quick link ("*3<target>") or unlink
// ("*1<target>") touch-tone command from Home's quick actions — the
// same underlying mechanism as the node page's touch-tone sender
// (asterisk -rx "rpt fun <node> <digits>"), scoped to just these two
// standard AllStarLink codes rather than an arbitrary typed sequence,
// since this is meant to be a one-click shortcut.
//
// It answers in JSON when the caller asks (the home page's fetch does),
// so the command can be sent without a full-page reload — the live SSE
// stream then reflects the connection appearing or dropping. A plain
// form POST (no JS) still works and re-renders Home with a flash, so the
// action degrades gracefully.
func (s *Server) handleNodeLink(w http.ResponseWriter, r *http.Request) {
	number := r.PathValue("number")
	wantsJSON := strings.Contains(r.Header.Get("Accept"), "application/json")

	fail := func(msg string) {
		if wantsJSON {
			writeJSON(w, map[string]any{"ok": false, "message": msg})
			return
		}
		s.renderHome(w, r, flash("error", msg))
	}

	if err := r.ParseForm(); err != nil {
		fail("bad form")
		return
	}
	target := strings.TrimSpace(r.FormValue("target"))
	if target == "" {
		fail("Enter a node number to link or unlink")
		return
	}

	var digits string
	switch r.FormValue("action") {
	case "link":
		digits = "*3" + target
	case "unlink":
		digits = "*1" + target
	default:
		fail("Unknown action")
		return
	}

	out, err := system.AsteriskRX(r.Context(), s.asteriskBin, "rpt fun "+number+" "+digits)
	if err != nil {
		fail(err.Error())
		return
	}
	msg := "Sent " + digits + " to node " + number
	if trimmed := strings.TrimSpace(out); trimmed != "" {
		msg += ": " + trimmed
	}
	if wantsJSON {
		writeJSON(w, map[string]any{"ok": true, "message": msg})
		return
	}
	s.renderHome(w, r, flash("ok", msg))
}

// handleNodeRecreateDevice is Home's one-click failsafe for the exact
// failure this project hit on real hardware: a node's configured radio
// device stanza went missing — apparently from something outside this
// app regenerating usbradio.conf/simpleusb.conf, e.g. HamVoIP's
// node-config.sh, which documents itself as overwriting rpt.conf on
// every run — which stops chan_simpleusb/chan_usbradio from loading
// and Asterisk from starting at all.
//
// This recreates the missing stanza with generic, safe starting-point
// values, not the operator's actual tuned audio levels — those can't be
// recovered once the section is gone, so re-tuning afterward (e.g. via
// HamVoIP's own simpleusb-tune-menu) is expected, not optional. This
// only runs when clicked; nothing here happens automatically.
func (s *Server) handleNodeRecreateDevice(w http.ResponseWriter, r *http.Request) {
	number := r.PathValue("number")
	node, err := s.store.LoadNode(number)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ref, ok := parseRadioChannel(node.RXChannel)
	if !ok {
		s.renderHome(w, r, flash("error", "Node "+number+"'s radio device isn't a recognized USB driver type — nothing to recreate"))
		return
	}
	if devices, err := s.store.ListRadioDevices(ref.File); err == nil && slices.Contains(devices, ref.Name) {
		s.renderHome(w, r, flash("ok", "Device "+ref.Name+" already exists in "+ref.File+" — nothing to do"))
		return
	}

	if err := s.store.SaveRadioDevice(ref.File, placeholderRadioDevice(ref.Name)); err != nil {
		s.renderHome(w, r, flash("error", err.Error()))
		return
	}

	msg := "Recreated device \"" + ref.Name + "\" in " + ref.File + " with generic starting-point levels (500/500) — the original tuned audio levels couldn't be recovered. Fine-tune it from the System page, or re-run simpleusb-tune-menu."
	s.renderHome(w, r, flash("ok", msg+" "+s.recheckAsteriskMessage(r.Context())))
}

// recheckAsteriskMessage gives safe_asterisk's own retry loop a moment
// to notice a config fix and bring Asterisk up before reporting back,
// so the caller's flash message reflects reality rather than the stale
// pre-fix state.
func (s *Server) recheckAsteriskMessage(ctx context.Context) string {
	time.Sleep(4 * time.Second)
	if system.AsteriskRunning(ctx, s.asteriskBin) {
		return "Asterisk is now running."
	}
	return "Asterisk is still not running — check asterisk -cvvvvv for what's blocking it now."
}
