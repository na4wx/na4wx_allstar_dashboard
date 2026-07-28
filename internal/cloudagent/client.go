package cloudagent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// runOnce dials settings.CloudURL, performs the hello/helloAck
// handshake, and — if accepted — services the connection (heartbeat +
// relayed calls) until it ends for any reason. The returned bool
// reports only whether the hello handshake itself succeeded, which is
// what Run uses to decide whether to reset its backoff: a connection
// that hellos successfully and then drops five minutes later should
// retry quickly, but a connection that never gets past a rejected API
// key should keep backing off.
func (a *Agent) runOnce(ctx context.Context, settings Settings) (helloSucceeded bool) {
	conn, _, err := websocket.Dial(ctx, settings.CloudURL, nil)
	if err != nil {
		logf("dial %s: %v", settings.CloudURL, err)
		return false
	}
	// coder/websocket defaults to a 32KiB read limit -- far too small for
	// anything but a very short relayed audio clip (sounds.upload's
	// inbound base64 params, in particular). A message over the limit
	// doesn't degrade gracefully; the read fails and the whole connection
	// is torn down. 16MiB comfortably covers any single sound file this
	// app expects to relay.
	conn.SetReadLimit(maxRelayMessageBytes)
	// Registered even before the hello handshake completes, so Reload's
	// local kill switch (see its doc comment) can cut the connection at
	// any point, not just once fully established.
	a.setActiveConn(conn)
	defer a.setActiveConn(nil)
	defer conn.CloseNow()

	nodes, err := a.store.ListNodes()
	if err != nil {
		logf("list nodes: %v", err)
	}

	helloCtx, cancel := context.WithTimeout(ctx, helloTimeout)
	err = wsjson.Write(helloCtx, conn, envelope{Type: typeHello, APIKey: settings.APIKey, Nodes: nodes})
	cancel()
	if err != nil {
		logf("send hello: %v", err)
		return false
	}

	ackCtx, cancel := context.WithTimeout(ctx, helloTimeout)
	var ack envelope
	err = wsjson.Read(ackCtx, conn, &ack)
	cancel()
	if err != nil {
		logf("read helloAck: %v", err)
		return false
	}
	if ack.Type != typeHelloAck || !ack.OK {
		logf("connection rejected: %s", ack.Error)
		return false
	}
	logf("connected to %s", settings.CloudURL)
	a.mu.Lock()
	a.lastConnected = time.Now()
	a.ownerSubscriptionActive = ack.OwnerSubscriptionActive
	a.mu.Unlock()

	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go a.heartbeatLoop(heartbeatCtx, conn)

	a.readLoop(ctx, conn)
	return true
}

// heartbeatLoop pushes the read-only status event on heartbeatInterval
// until ctx is cancelled (the connection ending, or Run tearing this
// session down). Sends one immediately on connect rather than waiting a
// full interval, so the cloud's device list shows a fresh reading right
// away.
func (a *Agent) heartbeatLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	send := func() {
		result, err := a.dispatch(ctx, "system.status", nil)
		if err != nil {
			return
		}
		data, err := json.Marshal(result)
		if err != nil {
			return
		}
		writeCtx, cancel := context.WithTimeout(ctx, helloTimeout)
		defer cancel()
		if err := wsjson.Write(writeCtx, conn, envelope{Type: typeEvent, Event: eventStatus, Data: data}); err != nil {
			return
		}
	}

	send()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

// readLoop reads relayed messages until the connection ends (error, or
// ctx cancelled), dispatching each "call" concurrently so one slow
// action can't stall replies to others queued behind it. Always tears
// down every active live watch on the way out (see liveWatches.stopAll)
// so a dropped connection never leaves a poller running for a cloud
// session that's already gone.
func (a *Agent) readLoop(ctx context.Context, conn *websocket.Conn) {
	defer a.live.stopAll()
	for {
		var msg envelope
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			return
		}
		switch msg.Type {
		case typeCall:
			go a.handleCall(ctx, conn, msg)
		case typeWatch:
			a.live.watch(ctx, a, conn, msg.Node)
		case typeUnwatch:
			a.live.unwatch(msg.Node)
		}
	}
}

// handleCall runs one relayed action and writes its correlated result
// back. Per this package's doc comment, "ok" absent means false — the
// presence of Error is what a caller checks, matching how a missing
// "ok":true is written back for every non-error result too (envelope's
// OK field is only ever set true here; false stays implicit via
// omitempty, alongside the Error string that explains why).
func (a *Agent) handleCall(ctx context.Context, conn *websocket.Conn, msg envelope) {
	result, err := a.dispatch(ctx, msg.Action, msg.Params)
	reply := envelope{Type: typeResult, ID: msg.ID}
	if err != nil {
		reply.Error = err.Error()
	} else {
		data, merr := json.Marshal(result)
		if merr != nil {
			reply.Error = merr.Error()
		} else {
			reply.OK = true
			reply.Data = data
		}
	}
	writeCtx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()
	_ = wsjson.Write(writeCtx, conn, reply)
}
