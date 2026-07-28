package cloudagent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"hamvoipconfiggui/internal/config"
	"hamvoipconfiggui/internal/sounds"
)

// startFakeCloud runs handler against every WebSocket connection to a
// local test server and returns its ws:// URL, standing in for the real
// cloud site's /agent endpoint for these tests.
func startFakeCloud(t *testing.T, handler func(ctx context.Context, conn *websocket.Conn)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		handler(r.Context(), conn)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// TestRunOnceHelloAckAndHeartbeat is the end-to-end happy path over a
// real WebSocket connection: hello is sent with the right API key, and
// once the fake cloud acks it, a status heartbeat event arrives without
// waiting for heartbeatInterval (heartbeatLoop sends one immediately on
// connect).
func TestRunOnceHelloAckAndHeartbeat(t *testing.T) {
	heartbeats := make(chan envelope, 4)
	var helloSeen envelope
	url := startFakeCloud(t, func(ctx context.Context, conn *websocket.Conn) {
		if err := wsjson.Read(ctx, conn, &helloSeen); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, envelope{Type: typeHelloAck, OK: true}); err != nil {
			return
		}
		for {
			var msg envelope
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				return
			}
			if msg.Type == typeEvent && msg.Event == eventStatus {
				heartbeats <- msg
			}
		}
	})

	a := newTestAgent(t, t.TempDir()+"/settings.json", config.NewStore(t.TempDir()), "does-not-exist-asterisk-binary")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan bool, 1)
	go func() {
		done <- a.runOnce(ctx, Settings{CloudURL: url, APIKey: "test-key", Enabled: true})
	}()

	select {
	case <-heartbeats:
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive a heartbeat event")
	}
	if helloSeen.Type != typeHello || helloSeen.APIKey != "test-key" {
		t.Errorf("hello = %+v, want type=hello apiKey=test-key", helloSeen)
	}

	cancel()
	if ok := <-done; !ok {
		t.Error("runOnce() = false, want true (hello handshake succeeded)")
	}
}

// TestRunOnceSetsLastConnected confirms LastConnected -- surfaced on
// the local Cloud Sync settings card as a liveness signal -- moves from
// the zero Time to "just now" the moment a hello handshake succeeds,
// not merely when a connection is dialed.
func TestRunOnceSetsLastConnected(t *testing.T) {
	url := startFakeCloud(t, func(ctx context.Context, conn *websocket.Conn) {
		var hello envelope
		if err := wsjson.Read(ctx, conn, &hello); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, envelope{Type: typeHelloAck, OK: true}); err != nil {
			return
		}
		<-ctx.Done()
	})

	a := newTestAgent(t, t.TempDir()+"/settings.json", config.NewStore(t.TempDir()), "does-not-exist-asterisk-binary")
	if !a.LastConnected().IsZero() {
		t.Fatal("LastConnected() is non-zero before any connection attempt")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	before := time.Now()
	done := make(chan bool, 1)
	go func() {
		done <- a.runOnce(ctx, Settings{CloudURL: url, APIKey: "test-key", Enabled: true})
	}()

	deadline := time.After(2 * time.Second)
	for a.LastConnected().IsZero() {
		select {
		case <-deadline:
			t.Fatal("LastConnected() still zero after the hello handshake should have succeeded")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if last := a.LastConnected(); last.Before(before) {
		t.Errorf("LastConnected() = %v, want a time at or after %v", last, before)
	}

	cancel()
	<-done
}

// TestRunOnceCapturesOwnerSubscriptionActive confirms a helloAck's
// ownerSubscriptionActive flag is captured onto the Agent, readable via
// OwnerSubscriptionActive() -- the signal home.html uses to swap its
// subscribe pitch for a thank-you.
func TestRunOnceCapturesOwnerSubscriptionActive(t *testing.T) {
	url := startFakeCloud(t, func(ctx context.Context, conn *websocket.Conn) {
		var hello envelope
		if err := wsjson.Read(ctx, conn, &hello); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, envelope{Type: typeHelloAck, OK: true, OwnerSubscriptionActive: true}); err != nil {
			return
		}
		<-ctx.Done()
	})

	a := newTestAgent(t, t.TempDir()+"/settings.json", config.NewStore(t.TempDir()), "does-not-exist-asterisk-binary")
	if a.OwnerSubscriptionActive() {
		t.Fatal("OwnerSubscriptionActive() is true before any connection attempt")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan bool, 1)
	go func() {
		done <- a.runOnce(ctx, Settings{CloudURL: url, APIKey: "test-key", Enabled: true})
	}()

	deadline := time.After(2 * time.Second)
	for !a.OwnerSubscriptionActive() {
		select {
		case <-deadline:
			t.Fatal("OwnerSubscriptionActive() still false after a helloAck with ownerSubscriptionActive=true")
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	<-done
}

// TestRunOnceRejectedHelloReturnsFalse confirms a rejected API key
// reports helloSucceeded=false, which is what Run uses to keep backing
// off instead of resetting to the initial 1s delay.
func TestRunOnceRejectedHelloReturnsFalse(t *testing.T) {
	url := startFakeCloud(t, func(ctx context.Context, conn *websocket.Conn) {
		var hello envelope
		if err := wsjson.Read(ctx, conn, &hello); err != nil {
			return
		}
		_ = wsjson.Write(ctx, conn, envelope{Type: typeHelloAck, OK: false, Error: "invalid api key"})
	})

	a := newTestAgent(t, t.TempDir()+"/settings.json", config.NewStore(t.TempDir()), "asterisk")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if ok := a.runOnce(ctx, Settings{CloudURL: url, APIKey: "bad-key", Enabled: true}); ok {
		t.Error("runOnce() = true, want false when the cloud rejects the hello")
	}
}

// TestRunOnceHandlesRelayedCall exercises the actual relay path a real
// cloud backend would use: a "call" message for a registered action
// gets a correlated "result" back with the right id and marshaled data.
func TestRunOnceHandlesRelayedCall(t *testing.T) {
	results := make(chan envelope, 1)
	url := startFakeCloud(t, func(ctx context.Context, conn *websocket.Conn) {
		var hello envelope
		if err := wsjson.Read(ctx, conn, &hello); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, envelope{Type: typeHelloAck, OK: true}); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, envelope{Type: typeCall, ID: "call-1", Action: "system.status"}); err != nil {
			return
		}
		for {
			var msg envelope
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				return
			}
			if msg.Type == typeResult && msg.ID == "call-1" {
				results <- msg
				return
			}
		}
	})

	a := newTestAgent(t, t.TempDir()+"/settings.json", config.NewStore(t.TempDir()), "does-not-exist-asterisk-binary")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go a.runOnce(ctx, Settings{CloudURL: url, APIKey: "test-key", Enabled: true})

	select {
	case res := <-results:
		if !res.OK || res.Error != "" {
			t.Errorf("result = %+v, want ok=true and no error", res)
		}
		if len(res.Data) == 0 {
			t.Error("result.Data is empty, want the marshaled system.Status")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive a result for the relayed call")
	}
}

// TestRunOnceHandlesLargeRelayedParams is the regression test for
// runOnce's conn.SetReadLimit call: coder/websocket defaults to a 32KiB
// per-message read limit, which a real sounds.upload call (base64-
// inflated audio bytes) blows past easily -- a message over the limit
// used to fail the read and tear down the whole connection instead of
// just failing that one call. This sends a >32KB "call" envelope and
// confirms a correlated "result" still comes back rather than the
// connection dying.
func TestRunOnceHandlesLargeRelayedParams(t *testing.T) {
	// newSoundsTestAgent's store is nil (fine for dispatching sounds.*
	// actions directly), but runOnce itself calls a.store.ListNodes()
	// unconditionally as part of hello -- needs a real Store here.
	customDir, stockDir := t.TempDir(), t.TempDir()
	soundsStore := sounds.New(customDir, stockDir, fakeSoxTool(t))
	a := New(t.TempDir()+"/settings.json", "", config.NewStore(t.TempDir()), "asterisk", soundsStore, nil, nil, "", "818-prog", "", "")

	largeData := bytes.Repeat([]byte("x"), 40*1024) // 40KB raw -> well over 32KB once base64-inflated and wrapped in JSON
	params, err := json.Marshal(map[string]string{
		"name":       "big-test",
		"dataBase64": base64.StdEncoding.EncodeToString(largeData),
	})
	if err != nil {
		t.Fatal(err)
	}

	results := make(chan envelope, 1)
	url := startFakeCloud(t, func(ctx context.Context, conn *websocket.Conn) {
		var hello envelope
		if err := wsjson.Read(ctx, conn, &hello); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, envelope{Type: typeHelloAck, OK: true}); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, envelope{Type: typeCall, ID: "call-1", Action: "sounds.upload", Params: params}); err != nil {
			return
		}
		for {
			var msg envelope
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				return
			}
			if msg.Type == typeResult && msg.ID == "call-1" {
				results <- msg
				return
			}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go a.runOnce(ctx, Settings{CloudURL: url, APIKey: "test-key", Enabled: true})

	select {
	case res := <-results:
		if !res.OK || res.Error != "" {
			t.Errorf("result = %+v, want ok=true and no error (a >32KB message must not kill the connection)", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive a result for the large relayed call -- the connection likely died reading it")
	}
}

// TestReloadClosesActiveConnection is the local kill-switch guarantee
// (see Agent.Reload's doc comment): disabling Cloud Sync, or changing
// its settings, while already connected must cut that connection right
// away, not just stop future reconnect attempts. Without this,
// physical/local access to the node wouldn't reliably override a
// still-open session.
func TestReloadClosesActiveConnection(t *testing.T) {
	heartbeats := make(chan envelope, 1)
	url := startFakeCloud(t, func(ctx context.Context, conn *websocket.Conn) {
		var hello envelope
		if err := wsjson.Read(ctx, conn, &hello); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, envelope{Type: typeHelloAck, OK: true}); err != nil {
			return
		}
		for {
			var msg envelope
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				return
			}
			if msg.Type == typeEvent && msg.Event == eventStatus {
				select {
				case heartbeats <- msg:
				default:
				}
			}
		}
	})

	a := newTestAgent(t, t.TempDir()+"/settings.json", config.NewStore(t.TempDir()), "does-not-exist-asterisk-binary")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan bool, 1)
	go func() {
		done <- a.runOnce(ctx, Settings{CloudURL: url, APIKey: "test-key", Enabled: true})
	}()

	select {
	case <-heartbeats:
	case <-time.After(2 * time.Second):
		t.Fatal("connection never became active")
	}

	start := time.Now()
	a.Reload()

	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("runOnce took %v to return after Reload, want promptly", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runOnce did not return after Reload -- the local kill switch didn't actually close the connection")
	}
}

// TestRunOnceUnknownActionReturnsErrorResult confirms an action name
// the registry doesn't recognize comes back as an error result, not a
// dropped message or a panic.
func TestRunOnceUnknownActionReturnsErrorResult(t *testing.T) {
	results := make(chan envelope, 1)
	url := startFakeCloud(t, func(ctx context.Context, conn *websocket.Conn) {
		var hello envelope
		if err := wsjson.Read(ctx, conn, &hello); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, envelope{Type: typeHelloAck, OK: true}); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, envelope{Type: typeCall, ID: "call-2", Action: "system.reboot"}); err != nil {
			return
		}
		for {
			var msg envelope
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				return
			}
			if msg.Type == typeResult && msg.ID == "call-2" {
				results <- msg
				return
			}
		}
	})

	a := newTestAgent(t, t.TempDir()+"/settings.json", config.NewStore(t.TempDir()), "does-not-exist-asterisk-binary")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go a.runOnce(ctx, Settings{CloudURL: url, APIKey: "test-key", Enabled: true})

	select {
	case res := <-results:
		if res.OK {
			t.Error("result.OK = true, want false for an action not in the registry")
		}
		if res.Error == "" {
			t.Error("result.Error is empty, want an explanation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive a result for the relayed call")
	}
}
