package internal

import (
	"sync"

	"github.com/aweager/jrpc-mesh/pkg/mesh"
	"github.com/sourcegraph/jsonrpc2"
)

// SubscriptionTable tracks which connections are subscribed to which
// (publisher, topic) pairs.
type SubscriptionTable struct {
	mu         sync.RWMutex
	bySub      map[mesh.Subscription]map[*jsonrpc2.Conn]struct{}
	byConn     map[*jsonrpc2.Conn]map[mesh.Subscription]struct{}
}

// NewSubscriptionTable creates a new empty SubscriptionTable.
func NewSubscriptionTable() *SubscriptionTable {
	return &SubscriptionTable{
		bySub:  make(map[mesh.Subscription]map[*jsonrpc2.Conn]struct{}),
		byConn: make(map[*jsonrpc2.Conn]map[mesh.Subscription]struct{}),
	}
}

// Update replaces the connection's subscription set with the given list.
func (st *SubscriptionTable) Update(conn *jsonrpc2.Conn, subs []mesh.Subscription) {
	st.mu.Lock()
	defer st.mu.Unlock()

	desired := make(map[mesh.Subscription]struct{}, len(subs))
	for _, s := range subs {
		desired[s] = struct{}{}
	}

	current := st.byConn[conn]
	for s := range current {
		if _, keep := desired[s]; !keep {
			if subscribers := st.bySub[s]; subscribers != nil {
				delete(subscribers, conn)
				if len(subscribers) == 0 {
					delete(st.bySub, s)
				}
			}
			delete(current, s)
		}
	}

	for s := range desired {
		if _, exists := current[s]; exists {
			continue
		}
		if current == nil {
			current = make(map[mesh.Subscription]struct{})
			st.byConn[conn] = current
		}
		current[s] = struct{}{}
		subscribers, ok := st.bySub[s]
		if !ok {
			subscribers = make(map[*jsonrpc2.Conn]struct{})
			st.bySub[s] = subscribers
		}
		subscribers[conn] = struct{}{}
	}

	if len(current) == 0 {
		delete(st.byConn, conn)
	}
}

// Subscribers returns a snapshot of the connections subscribed to sub.
func (st *SubscriptionTable) Subscribers(sub mesh.Subscription) []*jsonrpc2.Conn {
	st.mu.RLock()
	defer st.mu.RUnlock()

	subscribers := st.bySub[sub]
	if len(subscribers) == 0 {
		return nil
	}
	out := make([]*jsonrpc2.Conn, 0, len(subscribers))
	for c := range subscribers {
		out = append(out, c)
	}
	return out
}

// RemoveConn removes all subscriptions for the given connection.
func (st *SubscriptionTable) RemoveConn(conn *jsonrpc2.Conn) {
	st.Update(conn, nil)
}
