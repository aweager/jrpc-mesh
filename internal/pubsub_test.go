package internal

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/aweager/jrpc-mesh/pkg/mesh"
	"github.com/sourcegraph/jsonrpc2"
)

// receivedMessage is a captured ReceiveMessage notification.
type receivedMessage struct {
	Publisher string          `json:"publisher"`
	Topic     string          `json:"topic"`
	Payload   json.RawMessage `json:"payload"`
}

// newSubscriber creates a service-style connection that records all
// awe.proxy.client/ReceiveMessage notifications. Returns the service-side
// connection and a function that returns a snapshot of received messages.
func newSubscriber(t *testing.T, h *Handler, subs []mesh.Subscription) (*jsonrpc2.Conn, func() []receivedMessage) {
	t.Helper()

	var mu sync.Mutex
	var received []receivedMessage

	handler := jsonrpc2.HandlerWithError(func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
		if req.Method != "awe.proxy.client/ReceiveMessage" {
			return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeMethodNotFound, Message: "not found"}
		}
		var msg receivedMessage
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &msg); err != nil {
				return nil, err
			}
		}
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
		return nil, nil
	})

	svc := newTestService(t, h, nil, handler)

	if len(subs) > 0 {
		var result struct{}
		err := svc.client.Call(context.Background(), "awe.proxy/UpdateSubscriptions",
			mesh.UpdateSubscriptionsParams{Subscriptions: subs}, &result)
		if err != nil {
			t.Fatalf("UpdateSubscriptions failed: %v", err)
		}
	}

	snapshot := func() []receivedMessage {
		mu.Lock()
		defer mu.Unlock()
		out := make([]receivedMessage, len(received))
		copy(out, received)
		return out
	}

	return svc.client, snapshot
}

func publish(t *testing.T, conn *jsonrpc2.Conn, publisher, topic string, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var result struct{}
	err = conn.Call(context.Background(), "awe.proxy/PublishMessage", mesh.PublishMessageParams{
		Publisher: publisher,
		Topic:     topic,
		Payload:   raw,
	}, &result)
	if err != nil {
		t.Fatalf("PublishMessage failed: %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timeout waiting for: " + msg)
}

func TestPublish_DeliversToLocalSubscriber(t *testing.T) {
	h := NewHandler()

	_, snapshot := newSubscriber(t, h, []mesh.Subscription{
		{Publisher: "origin/svc/", Topic: "events"},
	})

	pub, _ := testConn(t, h)
	publish(t, pub, "origin/svc/", "events", map[string]string{"v": "1"})

	waitFor(t, func() bool { return len(snapshot()) == 1 }, "one message delivered")

	msgs := snapshot()
	if msgs[0].Publisher != "origin/svc/" || msgs[0].Topic != "events" {
		t.Errorf("unexpected msg: %+v", msgs[0])
	}
	var payload map[string]string
	if err := json.Unmarshal(msgs[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["v"] != "1" {
		t.Errorf("payload = %v, want v=1", payload)
	}
}

func TestPublish_NoSubscribersIsNoOp(t *testing.T) {
	h := NewHandler()
	pub, _ := testConn(t, h)
	publish(t, pub, "origin/svc/", "events", map[string]string{"v": "1"})
	// No assertion needed — should simply not error.
}

func TestUpdateSubscriptions_ReplacesPreviousSet(t *testing.T) {
	h := NewHandler()

	subClient, snapshot := newSubscriber(t, h, []mesh.Subscription{
		{Publisher: "origin/svc/", Topic: "a"},
	})

	// Replace subscriptions: drop "a", add "b".
	var result struct{}
	err := subClient.Call(context.Background(), "awe.proxy/UpdateSubscriptions",
		mesh.UpdateSubscriptionsParams{Subscriptions: []mesh.Subscription{
			{Publisher: "origin/svc/", Topic: "b"},
		}}, &result)
	if err != nil {
		t.Fatalf("UpdateSubscriptions failed: %v", err)
	}

	pub, _ := testConn(t, h)
	publish(t, pub, "origin/svc/", "a", map[string]string{"v": "old"})
	publish(t, pub, "origin/svc/", "b", map[string]string{"v": "new"})

	waitFor(t, func() bool { return len(snapshot()) == 1 }, "exactly one message")

	msgs := snapshot()
	if msgs[0].Topic != "b" {
		t.Errorf("expected topic=b, got %s", msgs[0].Topic)
	}
}

func TestPublish_OnlyExactMatchDelivered(t *testing.T) {
	h := NewHandler()

	_, snapshot := newSubscriber(t, h, []mesh.Subscription{
		{Publisher: "origin/svc/", Topic: "events"},
	})

	pub, _ := testConn(t, h)
	// Wrong publisher.
	publish(t, pub, "other/svc/", "events", map[string]int{"x": 1})
	// Wrong topic.
	publish(t, pub, "origin/svc/", "other", map[string]int{"x": 2})
	// Match.
	publish(t, pub, "origin/svc/", "events", map[string]int{"x": 3})

	waitFor(t, func() bool { return len(snapshot()) == 1 }, "only the matching publish delivered")
}

func TestPublish_DeliversToMultipleLocalSubscribers(t *testing.T) {
	h := NewHandler()

	sub := mesh.Subscription{Publisher: "origin/svc/", Topic: "events"}
	_, snap1 := newSubscriber(t, h, []mesh.Subscription{sub})
	_, snap2 := newSubscriber(t, h, []mesh.Subscription{sub})

	pub, _ := testConn(t, h)
	publish(t, pub, "origin/svc/", "events", map[string]string{"v": "1"})

	waitFor(t, func() bool { return len(snap1()) == 1 && len(snap2()) == 1 }, "both subscribers received")
}

func TestPublish_ForwardedToPeerSubscribers(t *testing.T) {
	// P2 is the peer that holds the subscriber.
	peerSocket, peerHandler, cleanup := testPeerProxy(t)
	defer cleanup()

	_, peerSnapshot := newSubscriber(t, peerHandler, []mesh.Subscription{
		{Publisher: "origin/svc/", Topic: "events"},
	})

	// P1 connects to P2 as peer.
	h := NewHandler()
	control, _ := testConn(t, h)
	var result struct{}
	err := control.Call(context.Background(), "awe.proxy/AddPeerProxy",
		mesh.AddPeerProxyParams{Socket: peerSocket}, &result)
	if err != nil {
		t.Fatalf("AddPeerProxy failed: %v", err)
	}

	// A local client on P1 publishes — should be delivered to P2's subscriber.
	pub, _ := testConn(t, h)
	publish(t, pub, "origin/svc/", "events", map[string]string{"v": "peer"})

	waitFor(t, func() bool { return len(peerSnapshot()) == 1 }, "peer subscriber received forwarded publish")
}

func TestPublish_PeerOriginatedNotReforwarded(t *testing.T) {
	// Three proxies: P1 (local), P2 (peer hub), P3 (other peer of P2).
	// A publish originating on P1 reaches P2 via forwarding, but P2 must
	// NOT re-forward to P3 (loop prevention).
	peerSocket, _, cleanup := testPeerProxy(t)
	defer cleanup()

	// P1 with a local subscriber.
	h := NewHandler()
	_, p1Snapshot := newSubscriber(t, h, []mesh.Subscription{
		{Publisher: "origin/svc/", Topic: "events"},
	})

	// P3 with a subscriber — used to detect any wrongful re-forward via P2.
	p3Handler := NewHandler()
	_, p3Snapshot := newSubscriber(t, p3Handler, []mesh.Subscription{
		{Publisher: "origin/svc/", Topic: "events"},
	})

	// Attach both P1 and P3 to P2.
	p3Control, _ := testConn(t, p3Handler)
	if err := p3Control.Call(context.Background(), "awe.proxy/AddPeerProxy",
		mesh.AddPeerProxyParams{Socket: peerSocket}, &struct{}{}); err != nil {
		t.Fatalf("P3 AddPeerProxy failed: %v", err)
	}
	control, _ := testConn(t, h)
	if err := control.Call(context.Background(), "awe.proxy/AddPeerProxy",
		mesh.AddPeerProxyParams{Socket: peerSocket}, &struct{}{}); err != nil {
		t.Fatalf("P1 AddPeerProxy failed: %v", err)
	}

	// Publish from a client of P1.
	pub, _ := testConn(t, h)
	publish(t, pub, "origin/svc/", "events", map[string]string{"v": "x"})

	waitFor(t, func() bool { return len(p1Snapshot()) == 1 }, "P1 subscriber received")

	// Give any (wrongful) re-forward time to propagate.
	time.Sleep(100 * time.Millisecond)

	if got := len(p3Snapshot()); got != 0 {
		t.Errorf("P3 should not receive re-forwarded publishes, got %d", got)
	}
}

func TestSubscriptions_ClearedOnPeerDisconnect(t *testing.T) {
	peerSocket, _, cleanup := testPeerProxy(t)

	// P1 connects to peer.
	h := NewHandler()
	control, _ := testConn(t, h)
	err := control.Call(context.Background(), "awe.proxy/AddPeerProxy",
		mesh.AddPeerProxyParams{Socket: peerSocket}, &struct{}{})
	if err != nil {
		t.Fatalf("AddPeerProxy failed: %v", err)
	}

	// Find the peer connection on P1's side and register a subscription
	// directly on the SubscriptionTable to simulate having one.
	h.peerMu.RLock()
	var peerConn *jsonrpc2.Conn
	for p := range h.peers {
		peerConn = p
	}
	h.peerMu.RUnlock()
	if peerConn == nil {
		t.Fatal("expected one peer connection")
	}
	h.Subscriptions.Update(peerConn, []mesh.Subscription{
		{Publisher: "origin/svc/", Topic: "events"},
	})

	// Verify subscription is present.
	if got := len(h.Subscriptions.Subscribers(mesh.Subscription{Publisher: "origin/svc/", Topic: "events"})); got != 1 {
		t.Fatalf("expected 1 subscriber before disconnect, got %d", got)
	}

	// Disconnect the peer.
	cleanup()

	waitFor(t, func() bool {
		return len(h.Subscriptions.Subscribers(mesh.Subscription{Publisher: "origin/svc/", Topic: "events"})) == 0
	}, "subscription cleared on disconnect")
}
