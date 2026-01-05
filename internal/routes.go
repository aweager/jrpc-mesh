package internal

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/sourcegraph/jsonrpc2"
)

// ErrWaitTimeout is returned when WaitUntilRoutable times out.
var ErrWaitTimeout = errors.New("timeout waiting for method to become routable")

// RouteTable manages prefix-based routing to JSON-RPC connections.
type RouteTable struct {
	mu     sync.RWMutex
	cond   *sync.Cond
	routes map[string]*jsonrpc2.Conn // prefix -> connection
}

// ensureCond initializes the condition variable if needed.
// Must be called with mu held.
func (rt *RouteTable) ensureCond() {
	if rt.cond == nil {
		rt.cond = sync.NewCond(&rt.mu)
	}
}

// Update sets the prefixes for a connection, removing any previous prefixes
// for that connection that are not in the new list.
func (rt *RouteTable) Update(conn *jsonrpc2.Conn, prefixes []string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	rt.ensureCond()
	if rt.routes == nil {
		rt.routes = make(map[string]*jsonrpc2.Conn)
	}

	// Remove any existing prefixes for this connection that aren't in the new list
	newPrefixes := make(map[string]struct{}, len(prefixes))
	for _, prefix := range prefixes {
		newPrefixes[prefix] = struct{}{}
	}
	for prefix, c := range rt.routes {
		if c == conn {
			if _, keep := newPrefixes[prefix]; !keep {
				delete(rt.routes, prefix)
			}
		}
	}

	// Add/update the new prefixes
	for _, prefix := range prefixes {
		rt.routes[prefix] = conn
	}

	rt.cond.Broadcast()
}

// Lookup returns the connection for the longest matching prefix, or nil if none match.
func (rt *RouteTable) Lookup(method string) *jsonrpc2.Conn {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	return rt.lookupUnlocked(method)
}

// lookupUnlocked is like Lookup but assumes the lock is already held.
func (rt *RouteTable) lookupUnlocked(method string) *jsonrpc2.Conn {
	var bestPrefix string
	var bestConn *jsonrpc2.Conn

	for prefix, conn := range rt.routes {
		if strings.HasPrefix(method, prefix) && len(prefix) > len(bestPrefix) {
			bestPrefix = prefix
			bestConn = conn
		}
	}

	return bestConn
}

// RemoveConn removes all routes registered to the given connection.
func (rt *RouteTable) RemoveConn(conn *jsonrpc2.Conn) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	for prefix, c := range rt.routes {
		if c == conn {
			delete(rt.routes, prefix)
		}
	}
}

// WaitUntilRoutable blocks until the given method becomes routable or the timeout expires.
// Returns nil on success, ErrWaitTimeout on timeout, or the context's error if cancelled.
func (rt *RouteTable) WaitUntilRoutable(ctx context.Context, method string, timeout time.Duration) error {
	// Fast path: check if already routable
	if rt.Lookup(method) != nil {
		return nil
	}

	deadline := time.Now().Add(timeout)
	timedOut := false

	// Start a goroutine that will broadcast when timeout expires
	done := make(chan struct{})
	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			rt.mu.Lock()
			timedOut = true
			rt.ensureCond()
			rt.cond.Broadcast()
			rt.mu.Unlock()
		case <-done:
		case <-ctx.Done():
			rt.mu.Lock()
			rt.ensureCond()
			rt.cond.Broadcast()
			rt.mu.Unlock()
		}
	}()
	defer close(done)

	// Wait loop
	rt.mu.Lock()
	rt.ensureCond()
	for {
		if rt.lookupUnlocked(method) != nil {
			rt.mu.Unlock()
			return nil
		}

		if timedOut || time.Now().After(deadline) {
			rt.mu.Unlock()
			return ErrWaitTimeout
		}

		if ctx.Err() != nil {
			rt.mu.Unlock()
			return ctx.Err()
		}

		rt.cond.Wait()
	}
}