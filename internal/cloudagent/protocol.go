package cloudagent

import "encoding/json"

// envelope is the one message shape carried over the WebSocket
// connection in both directions. Which fields are populated depends on
// Type; see the doc comments below for each direction's actual shape.
// Keeping every message on one wire type (rather than a Go union/sum
// type over several structs) keeps encode/decode trivial on both this
// side and the TypeScript side of the tunnel, at the cost of most
// fields being irrelevant to any given message — an acceptable trade
// for a small, hand-written protocol like this one.
type envelope struct {
	Type string `json:"type"`

	// hello (node -> cloud): the opening handshake, sent once right
	// after the connection is established. App identifies which of the
	// two node apps sent this hello ("hamvoipconfiggui" or "asl3", see
	// appName in run.go) -- the cloud dashboard's feature set differs
	// between them (this app has IAX2 peer registration and standalone
	// radio-device management that ASL3 deliberately dropped), and it
	// has no other way to tell which one it's talking to. Omitted by an
	// older build that predates this field; the cloud treats that the
	// same as the newer, more limited app until this one redeploys.
	APIKey string `json:"apiKey,omitempty"`
	App    string `json:"app,omitempty"`

	// Nodes is also re-sent on every status event below (see
	// heartbeatLoop in client.go), not just hello -- the cloud's own
	// cached node list (Device.nodes) needs a way to notice a node
	// being added/renamed locally without this process's own
	// connection dropping and re-hello-ing, which is the common case
	// (editing rpt.conf through the local dashboard doesn't restart
	// this process).
	Nodes []string `json:"nodes,omitempty"`

	// helloAck (cloud -> node): the cloud's reply to hello. This isn't
	// in the plan's original wire sketch but is needed to implement its
	// "reset backoff only on a successful hello handshake, not bare TCP
	// connect" requirement -- the client has no other way to learn
	// whether the API key was actually accepted.
	OK    bool   `json:"ok,omitempty"`
	Error string `json:"error,omitempty"`

	// OwnerSubscriptionActive (cloud -> node, helloAck only): whether
	// this device's owner currently has a genuine active paid
	// subscription on the cloud side -- narrower than "entitled" (which
	// also covers an active free trial or a comped account; see the
	// cloud repo's server/src/services/entitlement.ts hasActiveSubscription
	// doc comment for exactly why the distinction matters here). Always
	// false when the cloud deployment has billing switched off. Read by
	// the local dashboard's home page to swap its "subscribe" pitch for
	// a thank-you once the operator has actually started paying.
	OwnerSubscriptionActive bool `json:"ownerSubscriptionActive,omitempty"`

	// call (cloud -> node) / result (node -> cloud): a relayed action
	// invocation and its correlated reply. ID ties a result back to the
	// call that produced it.
	ID     string          `json:"id,omitempty"`
	Action string          `json:"action,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`

	// event (node -> cloud): an unsolicited push, e.g. the periodic
	// status heartbeat.
	Event string `json:"event,omitempty"`

	// watch / unwatch (cloud -> node): subscribe/unsubscribe a node's
	// live status stream. Not used until Phase 3's on-demand live
	// watch; the field is defined now so the wire type is stable.
	Node string `json:"node,omitempty"`
}

// Envelope type discriminators — see envelope's field-group comments
// above for which other fields each one populates.
const (
	typeHello    = "hello"
	typeHelloAck = "helloAck"
	typeCall     = "call"
	typeResult   = "result"
	typeEvent    = "event"
	typeWatch    = "watch"
	typeUnwatch  = "unwatch"
)

// eventStatus is the periodic heartbeat event name (see run.go).
const eventStatus = "status"

// appName identifies this app in the hello handshake's App field -- see
// that field's own doc comment. The ASL3 rewrite (a separate binary,
// separate repo) sends "asl3" instead.
const appName = "hamvoipconfiggui"
