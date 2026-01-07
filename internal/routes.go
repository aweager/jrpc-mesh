package internal

import (
	"context"
	"strings"
	"sync"

	"github.com/sourcegraph/jsonrpc2"
)

// RouteTable manages prefix-based routing to JSON-RPC connections.
type RouteTable struct {
	mu              sync.RWMutex
	cond            *sync.Cond
	routes          map[string]*jsonrpc2.Conn // prefix -> connection
	OnRoutesChanged func()                    // Called after routes are modified (outside lock)
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
	callback := rt.OnRoutesChanged
	rt.mu.Unlock()

	// Call callback outside the lock to allow it to read routes
	if callback != nil {
		callback()
	}
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

// GetPrefixesExcluding returns all registered prefixes except those owned by excluded connections.
func (rt *RouteTable) GetPrefixesExcluding(exclude map[*jsonrpc2.Conn]bool) []string {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	var prefixes []string
	for prefix, conn := range rt.routes {
		if !exclude[conn] {
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}

// WaitUntilRoutable blocks until the given method becomes routable or the context is cancelled.
// Returns nil on success, or the context's error if cancelled.
func (rt *RouteTable) WaitUntilRoutable(ctx context.Context, method string) error {
	// Fast path: check if already routable
	if rt.Lookup(method) != nil {
		return nil
	}

	// Start a goroutine that will broadcast when context is done
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			rt.mu.Lock()
			rt.ensureCond()
			rt.cond.Broadcast()
			rt.mu.Unlock()
		case <-done:
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

		if ctx.Err() != nil {
			rt.mu.Unlock()
			return ctx.Err()
		}

		rt.cond.Wait()
	}
}