package internal

import (
	"context"
	"strings"
	"sync"

	"github.com/sourcegraph/jsonrpc2"
)

// RouteTable manages prefix-based routing to JSON-RPC connections.
type RouteTable struct {
	mu           sync.RWMutex
	routes       map[string]*jsonrpc2.Conn // prefix -> connection
	callbacks    map[int]func()            // Called after routes are modified (outside lock)
	nextCallback int                       // ID for next callback
}

// NewRouteTable creates a new RouteTable with all necessary fields initialized.
func NewRouteTable() *RouteTable {
	return &RouteTable{
		routes:    make(map[string]*jsonrpc2.Conn),
		callbacks: make(map[int]func()),
	}
}

// AddCallback registers a callback to be called when routes change.
// Returns a function to remove the callback.
func (rt *RouteTable) AddCallback(f func()) (remove func()) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	id := rt.nextCallback
	rt.nextCallback++
	rt.callbacks[id] = f

	return func() {
		rt.mu.Lock()
		defer rt.mu.Unlock()

		delete(rt.callbacks, id)
	}
}

// Update sets the prefixes for a connection, removing any previous prefixes
// for that connection that are not in the new list.
func (rt *RouteTable) Update(conn *jsonrpc2.Conn, prefixes []string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

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

	// Copy callbacks to call outside the lock
	callbacks := make([]func(), 0, len(rt.callbacks))
	for _, cb := range rt.callbacks {
		callbacks = append(callbacks, cb)
	}

	// Call callbacks outside the lock in a new goroutine to avoid blocking
	go func() {
		for _, cb := range callbacks {
			cb()
		}
	}()
}

// Lookup returns the connection for the longest matching prefix, or nil if none match.
func (rt *RouteTable) Lookup(method string) *jsonrpc2.Conn {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	return rt.lookupUnlocked(method)
}

// LookupWithPrefix returns the connection and matched prefix for the longest matching prefix.
// Returns (nil, "") if no match is found.
func (rt *RouteTable) LookupWithPrefix(method string) (*jsonrpc2.Conn, string) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	var bestPrefix string
	var bestConn *jsonrpc2.Conn

	for prefix, conn := range rt.routes {
		if strings.HasPrefix(method, prefix) && len(prefix) > len(bestPrefix) {
			bestPrefix = prefix
			bestConn = conn
		}
	}

	return bestConn, bestPrefix
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
	rt.Update(conn, []string{})
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

// GetAllPrefixes returns all registered prefixes.
func (rt *RouteTable) GetAllPrefixes() []string {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	prefixes := make([]string, 0, len(rt.routes))
	for prefix := range rt.routes {
		prefixes = append(prefixes, prefix)
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

	// Create a channel to signal when routes change
	notify := make(chan struct{}, 1)
	remove := rt.AddCallback(func() {
		select {
		case notify <- struct{}{}:
		default:
		}
	})
	defer remove()

	// Check again after registering callback (in case route was added between check and registration)
	if rt.Lookup(method) != nil {
		return nil
	}

	// Wait for either route to become available or context cancellation
	for {
		select {
		case <-notify:
			if rt.Lookup(method) != nil {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
