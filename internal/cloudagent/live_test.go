package cloudagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"hamvoipconfiggui/internal/config"
)

// liveTestNodeStore returns a config.Store with node "2000" configured.
// Every agent in this file needs this now that watch()/snapshotLiveNode
// validate the requested node against the store (see validNodeNumber's
// own doc comment) -- an empty store made every one of these node
// references look "unknown" and silently no-op.
func liveTestNodeStore(t *testing.T) *config.Store {
	t.Helper()
	asteriskDir := t.TempDir()
	fixture := "[2000]\n" +
		"rxchannel = SimpleUSB/usb\n" +
		"duplex = 1\n"
	if err := os.WriteFile(filepath.Join(asteriskDir, config.RptConfFile), []byte(fixture), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return config.NewStore(asteriskDir)
}

// TestSnapshotLiveNodeConnectedNeverMarshalsToNull covers a real crash:
// a nil Go slice marshals to JSON null, and the cloud client's
// live.connected.length crashed the browser tab on exactly that --
// "nothing connected right now" (asteriskBin missing here reproduces
// that same "no connections found" path) is an everyday state, not an
// error, and must always come across the wire as [], never null.
func TestSnapshotLiveNodeConnectedNeverMarshalsToNull(t *testing.T) {
	a := newTestAgent(t, t.TempDir()+"/settings.json", liveTestNodeStore(t), "does-not-exist-asterisk-binary")
	live := a.snapshotLiveNode(context.Background(), "2000")

	if live.Connected == nil {
		t.Fatal("live.Connected is nil, want a non-nil (possibly empty) slice")
	}
	data, err := json.Marshal(live)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(data), `"connected":null`) {
		t.Errorf("marshaled JSON contains \"connected\":null: %s", data)
	}
	if !strings.Contains(string(data), `"connected":[]`) {
		t.Errorf("marshaled JSON missing \"connected\":[]: %s", data)
	}
}

// TestWatchStartsPushingLiveEvents confirms a "watch" envelope actually
// starts a poller that pushes "event"/"nodeLive" data for that node,
// without waiting a full liveWatchPollInterval (poll sends immediately
// on start, matching heartbeatLoop's own "send once, then tick").
func TestWatchStartsPushingLiveEvents(t *testing.T) {
	liveEvents := make(chan envelope, 4)
	url := startFakeCloud(t, func(ctx context.Context, conn *websocket.Conn) {
		var hello envelope
		if err := wsjson.Read(ctx, conn, &hello); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, envelope{Type: typeHelloAck, OK: true}); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, envelope{Type: typeWatch, Node: "2000"}); err != nil {
			return
		}
		for {
			var msg envelope
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				return
			}
			if msg.Type == typeEvent && msg.Event == nodeLiveEvent {
				select {
				case liveEvents <- msg:
				default:
				}
			}
		}
	})

	a := newTestAgent(t, t.TempDir()+"/settings.json", liveTestNodeStore(t), "does-not-exist-asterisk-binary")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go a.runOnce(ctx, Settings{CloudURL: url, APIKey: "test-key", Enabled: true})

	select {
	case msg := <-liveEvents:
		if msg.Node != "2000" {
			t.Errorf("event.Node = %q, want 2000", msg.Node)
		}
		if len(msg.Data) == 0 {
			t.Error("event.Data is empty, want the marshaled liveNodeState")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive a nodeLive event after watching")
	}
}

// TestUnwatchStopsPushingLiveEvents confirms an "unwatch" actually stops
// the poller rather than leaving it running forever.
func TestUnwatchStopsPushingLiveEvents(t *testing.T) {
	var eventCount int
	liveEvents := make(chan envelope, 16)
	stopWatching := make(chan struct{})
	url := startFakeCloud(t, func(ctx context.Context, conn *websocket.Conn) {
		var hello envelope
		if err := wsjson.Read(ctx, conn, &hello); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, envelope{Type: typeHelloAck, OK: true}); err != nil {
			return
		}
		if err := wsjson.Write(ctx, conn, envelope{Type: typeWatch, Node: "2000"}); err != nil {
			return
		}
		go func() {
			<-stopWatching
			_ = wsjson.Write(ctx, conn, envelope{Type: typeUnwatch, Node: "2000"})
		}()
		for {
			var msg envelope
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				return
			}
			if msg.Type == typeEvent && msg.Event == nodeLiveEvent {
				select {
				case liveEvents <- msg:
				default:
				}
			}
		}
	})

	a := newTestAgent(t, t.TempDir()+"/settings.json", liveTestNodeStore(t), "does-not-exist-asterisk-binary")
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	go a.runOnce(ctx, Settings{CloudURL: url, APIKey: "test-key", Enabled: true})

	// Let a couple of events through, then unwatch.
	<-liveEvents
	close(stopWatching)

	// Drain whatever was already in flight, then confirm nothing new
	// arrives for a couple of poll intervals.
	drainDeadline := time.After(300 * time.Millisecond)
drain:
	for {
		select {
		case <-liveEvents:
			eventCount++
		case <-drainDeadline:
			break drain
		}
	}

	select {
	case <-liveEvents:
		t.Fatal("received a nodeLive event after unwatch -- the poller wasn't actually stopped")
	case <-time.After(2*liveWatchPollInterval + 500*time.Millisecond):
		// Good: no more events after waiting past two poll intervals.
	}
}

// TestWatchIsIdempotent confirms watching an already-watched node
// doesn't start a second, duplicate poller (which would double the
// event rate).
func TestWatchIsIdempotent(t *testing.T) {
	lw := newLiveWatches()
	a := newTestAgent(t, t.TempDir()+"/settings.json", liveTestNodeStore(t), "asterisk")

	url := startFakeCloud(t, func(ctx context.Context, conn *websocket.Conn) {
		<-ctx.Done()
	})
	conn, _, err := websocket.Dial(context.Background(), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lw.watch(ctx, a, conn, "2000")
	lw.mu.Lock()
	firstStop := lw.stops["2000"]
	lw.mu.Unlock()

	lw.watch(ctx, a, conn, "2000") // should be a no-op
	lw.mu.Lock()
	secondStop := lw.stops["2000"]
	count := len(lw.stops)
	lw.mu.Unlock()

	if count != 1 {
		t.Fatalf("stops has %d entries, want 1", count)
	}
	if firstStop != secondStop {
		t.Error("watch() replaced the existing poller instead of leaving it alone")
	}
}
