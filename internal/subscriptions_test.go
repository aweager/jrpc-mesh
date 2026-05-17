package internal

import (
	"sync"
	"testing"

	"github.com/aweager/jrpc-mesh/pkg/mesh"
	"github.com/sourcegraph/jsonrpc2"
)

func subscriberSet(conns []*jsonrpc2.Conn) map[*jsonrpc2.Conn]struct{} {
	out := make(map[*jsonrpc2.Conn]struct{}, len(conns))
	for _, c := range conns {
		out[c] = struct{}{}
	}
	return out
}

func TestSubscriptionTable_EmptyHasNoSubscribers(t *testing.T) {
	st := NewSubscriptionTable()
	if got := st.Subscribers(mesh.Subscription{Publisher: "a/", Topic: "t"}); got != nil {
		t.Errorf("Subscribers() = %v, want nil", got)
	}
}

func TestSubscriptionTable_UpdateAddsSubscription(t *testing.T) {
	st := NewSubscriptionTable()
	conn := &jsonrpc2.Conn{}

	sub := mesh.Subscription{Publisher: "a/", Topic: "t"}
	st.Update(conn, []mesh.Subscription{sub})

	subs := st.Subscribers(sub)
	if len(subs) != 1 || subs[0] != conn {
		t.Errorf("Subscribers() = %v, want [conn]", subs)
	}
}

func TestSubscriptionTable_UpdateReplacesPreviousSet(t *testing.T) {
	st := NewSubscriptionTable()
	conn := &jsonrpc2.Conn{}

	subA := mesh.Subscription{Publisher: "a/", Topic: "t"}
	subB := mesh.Subscription{Publisher: "b/", Topic: "t"}

	st.Update(conn, []mesh.Subscription{subA})
	st.Update(conn, []mesh.Subscription{subB})

	if got := st.Subscribers(subA); got != nil {
		t.Errorf("Subscribers(subA) = %v, want nil after replacement", got)
	}
	if got := st.Subscribers(subB); len(got) != 1 || got[0] != conn {
		t.Errorf("Subscribers(subB) = %v, want [conn]", got)
	}
}

func TestSubscriptionTable_UpdateMultipleConnsPerSubscription(t *testing.T) {
	st := NewSubscriptionTable()
	c1 := &jsonrpc2.Conn{}
	c2 := &jsonrpc2.Conn{}

	sub := mesh.Subscription{Publisher: "a/", Topic: "t"}
	st.Update(c1, []mesh.Subscription{sub})
	st.Update(c2, []mesh.Subscription{sub})

	got := subscriberSet(st.Subscribers(sub))
	want := subscriberSet([]*jsonrpc2.Conn{c1, c2})
	if len(got) != 2 || got[c1] != struct{}{} || got[c2] != struct{}{} {
		t.Errorf("Subscribers(sub) = %v, want %v", got, want)
	}
}

func TestSubscriptionTable_UpdateMultipleSubscriptionsPerConn(t *testing.T) {
	st := NewSubscriptionTable()
	conn := &jsonrpc2.Conn{}

	subA := mesh.Subscription{Publisher: "a/", Topic: "t"}
	subB := mesh.Subscription{Publisher: "b/", Topic: "t"}
	st.Update(conn, []mesh.Subscription{subA, subB})

	for _, sub := range []mesh.Subscription{subA, subB} {
		subs := st.Subscribers(sub)
		if len(subs) != 1 || subs[0] != conn {
			t.Errorf("Subscribers(%v) = %v, want [conn]", sub, subs)
		}
	}
}

func TestSubscriptionTable_UpdatePartialReplace(t *testing.T) {
	st := NewSubscriptionTable()
	conn := &jsonrpc2.Conn{}

	subA := mesh.Subscription{Publisher: "a/", Topic: "t"}
	subB := mesh.Subscription{Publisher: "b/", Topic: "t"}
	subC := mesh.Subscription{Publisher: "c/", Topic: "t"}

	st.Update(conn, []mesh.Subscription{subA, subB})
	// Keep B, drop A, add C.
	st.Update(conn, []mesh.Subscription{subB, subC})

	if got := st.Subscribers(subA); got != nil {
		t.Errorf("Subscribers(subA) = %v, want nil", got)
	}
	if got := st.Subscribers(subB); len(got) != 1 {
		t.Errorf("Subscribers(subB) = %v, want [conn]", got)
	}
	if got := st.Subscribers(subC); len(got) != 1 {
		t.Errorf("Subscribers(subC) = %v, want [conn]", got)
	}
}

func TestSubscriptionTable_UpdateEmptyClearsConn(t *testing.T) {
	st := NewSubscriptionTable()
	conn := &jsonrpc2.Conn{}

	sub := mesh.Subscription{Publisher: "a/", Topic: "t"}
	st.Update(conn, []mesh.Subscription{sub})
	st.Update(conn, nil)

	if got := st.Subscribers(sub); got != nil {
		t.Errorf("Subscribers(sub) = %v, want nil", got)
	}

	// Internal maps should be cleaned up (no entry for conn or sub).
	st.mu.RLock()
	defer st.mu.RUnlock()
	if _, ok := st.byConn[conn]; ok {
		t.Error("byConn should not retain entry for cleared conn")
	}
	if _, ok := st.bySub[sub]; ok {
		t.Error("bySub should not retain empty entry for sub")
	}
}

func TestSubscriptionTable_UpdateDoesNotAffectOtherConns(t *testing.T) {
	st := NewSubscriptionTable()
	c1 := &jsonrpc2.Conn{}
	c2 := &jsonrpc2.Conn{}

	sub := mesh.Subscription{Publisher: "a/", Topic: "t"}
	st.Update(c1, []mesh.Subscription{sub})
	st.Update(c2, []mesh.Subscription{sub})

	// Clearing c1 should leave c2 subscribed.
	st.Update(c1, nil)

	subs := st.Subscribers(sub)
	if len(subs) != 1 || subs[0] != c2 {
		t.Errorf("Subscribers(sub) = %v, want [c2] after clearing c1", subs)
	}
}

func TestSubscriptionTable_RemoveConn(t *testing.T) {
	st := NewSubscriptionTable()
	conn := &jsonrpc2.Conn{}

	subA := mesh.Subscription{Publisher: "a/", Topic: "t"}
	subB := mesh.Subscription{Publisher: "b/", Topic: "t"}
	st.Update(conn, []mesh.Subscription{subA, subB})

	st.RemoveConn(conn)

	if got := st.Subscribers(subA); got != nil {
		t.Errorf("Subscribers(subA) = %v, want nil after RemoveConn", got)
	}
	if got := st.Subscribers(subB); got != nil {
		t.Errorf("Subscribers(subB) = %v, want nil after RemoveConn", got)
	}
}

func TestSubscriptionTable_RemoveConnUnknownIsNoOp(t *testing.T) {
	st := NewSubscriptionTable()
	conn := &jsonrpc2.Conn{}
	// Should not panic.
	st.RemoveConn(conn)
}

func TestSubscriptionTable_UpdateIdempotent(t *testing.T) {
	st := NewSubscriptionTable()
	conn := &jsonrpc2.Conn{}

	sub := mesh.Subscription{Publisher: "a/", Topic: "t"}
	st.Update(conn, []mesh.Subscription{sub})
	st.Update(conn, []mesh.Subscription{sub})

	subs := st.Subscribers(sub)
	if len(subs) != 1 || subs[0] != conn {
		t.Errorf("Subscribers(sub) = %v, want exactly [conn]", subs)
	}
}

func TestSubscriptionTable_PublisherAndTopicAreDistinct(t *testing.T) {
	st := NewSubscriptionTable()
	conn := &jsonrpc2.Conn{}

	sub := mesh.Subscription{Publisher: "a/", Topic: "t1"}
	st.Update(conn, []mesh.Subscription{sub})

	// Different topic, same publisher → no match.
	if got := st.Subscribers(mesh.Subscription{Publisher: "a/", Topic: "t2"}); got != nil {
		t.Errorf("expected no subscribers for different topic, got %v", got)
	}
	// Different publisher, same topic → no match.
	if got := st.Subscribers(mesh.Subscription{Publisher: "b/", Topic: "t1"}); got != nil {
		t.Errorf("expected no subscribers for different publisher, got %v", got)
	}
}

func TestSubscriptionTable_ConcurrentUpdatesAndReads(t *testing.T) {
	st := NewSubscriptionTable()
	const numConns = 20
	const iterations = 100

	sub := mesh.Subscription{Publisher: "a/", Topic: "t"}
	conns := make([]*jsonrpc2.Conn, numConns)
	for i := range conns {
		conns[i] = &jsonrpc2.Conn{}
	}

	var wg sync.WaitGroup
	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func(c *jsonrpc2.Conn) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				st.Update(c, []mesh.Subscription{sub})
				_ = st.Subscribers(sub)
				st.Update(c, nil)
			}
		}(conns[i])
	}
	wg.Wait()

	// All conns cleared themselves at end — final state should be empty.
	if got := st.Subscribers(sub); got != nil {
		t.Errorf("Subscribers(sub) = %d conns, want 0", len(got))
	}
}
