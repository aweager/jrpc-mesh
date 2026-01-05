package internal

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/sourcegraph/jsonrpc2"
)

// CodeTimeout is the application-level error code for timeout errors.
const CodeTimeout = 1

// Handler implements jsonrpc2.Handler for the jrpc-mesh proxy.
type Handler struct {
	mu         sync.RWMutex
	routes     map[string]*jsonrpc2.Conn // prefix -> connection
	routesCond *sync.Cond                // broadcast when routes change
}

// UpdateRoutesParams defines the parameters for awe.proxy/UpdateRoutes.
type UpdateRoutesParams struct {
	Prefixes []string `json:"prefixes"`
}

// WaitUntilRoutableParams defines the parameters for awe.proxy/WaitUntilRoutable.
type WaitUntilRoutableParams struct {
	Method   string   `json:"method"`
	TimeoutS *float64 `json:"timeout_s,omitempty"`
}

// ensureCond initializes the routes condition variable if needed.
func (h *Handler) ensureCond() {
	if h.routesCond == nil {
		h.routesCond = sync.NewCond(&h.mu)
	}
}

// Handle processes incoming JSON RPC requests.
func (h *Handler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	// Handle proxy-specific methods
	if strings.HasPrefix(req.Method, "awe.proxy/") {
		h.handleProxyMethod(ctx, conn, req)
		return
	}

	// Route to appropriate backend service
	backend := h.findRoute(req.Method)
	if backend == nil {
		if !req.Notif {
			conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
				Code:    jsonrpc2.CodeMethodNotFound,
				Message: "no backend registered for method: " + req.Method,
			})
		}
		return
	}

	if req.Notif {
		backend.Notify(ctx, req.Method, req.Params)
		return
	}

	var result json.RawMessage
	if err := backend.Call(ctx, req.Method, req.Params, &result); err != nil {
		if rpcErr, ok := err.(*jsonrpc2.Error); ok {
			conn.ReplyWithError(ctx, req.ID, rpcErr)
		} else {
			conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
				Code:    jsonrpc2.CodeInternalError,
				Message: err.Error(),
			})
		}
		return
	}
	conn.Reply(ctx, req.ID, result)
}

func (h *Handler) handleProxyMethod(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	method := strings.TrimPrefix(req.Method, "awe.proxy/")

	switch method {
	case "UpdateRoutes":
		h.handleUpdateRoutes(ctx, conn, req)
	case "WaitUntilRoutable":
		h.handleWaitUntilRoutable(ctx, conn, req)
	default:
		if req.Notif {
			return
		}
		conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeMethodNotFound,
			Message: "unknown proxy method: " + req.Method,
		})
	}
}

func (h *Handler) handleUpdateRoutes(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params UpdateRoutesParams
	if err := json.Unmarshal(*req.Params, &params); err != nil {
		if !req.Notif {
			conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
				Code:    jsonrpc2.CodeInvalidParams,
				Message: "invalid params: " + err.Error(),
			})
		}
		return
	}

	h.mu.Lock()
	h.ensureCond()
	if h.routes == nil {
		h.routes = make(map[string]*jsonrpc2.Conn)
	}
	// Remove any existing prefixes for this connection that aren't in the new list
	newPrefixes := make(map[string]struct{}, len(params.Prefixes))
	for _, prefix := range params.Prefixes {
		newPrefixes[prefix] = struct{}{}
	}
	for prefix, c := range h.routes {
		if c == conn {
			if _, keep := newPrefixes[prefix]; !keep {
				delete(h.routes, prefix)
			}
		}
	}
	// Add/update the new prefixes
	for _, prefix := range params.Prefixes {
		h.routes[prefix] = conn
	}
	h.routesCond.Broadcast()
	h.mu.Unlock()

	if !req.Notif {
		conn.Reply(ctx, req.ID, struct{}{})
	}
}

// findRoute returns the connection for the longest matching prefix, or nil if none match.
func (h *Handler) findRoute(method string) *jsonrpc2.Conn {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var bestPrefix string
	var bestConn *jsonrpc2.Conn

	for prefix, conn := range h.routes {
		if strings.HasPrefix(method, prefix) && len(prefix) > len(bestPrefix) {
			bestPrefix = prefix
			bestConn = conn
		}
	}

	return bestConn
}

// RemoveRoutesForConn removes all routes registered to the given connection.
func (h *Handler) RemoveRoutesForConn(conn *jsonrpc2.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for prefix, c := range h.routes {
		if c == conn {
			delete(h.routes, prefix)
		}
	}
}

func (h *Handler) handleWaitUntilRoutable(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if req.Notif {
		return
	}

	var params WaitUntilRoutableParams
	if err := json.Unmarshal(*req.Params, &params); err != nil {
		conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInvalidParams,
			Message: "invalid params: " + err.Error(),
		})
		return
	}

	if params.Method == "" {
		conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInvalidParams,
			Message: "method is required",
		})
		return
	}

	// Default timeout is 5.0 seconds
	timeoutS := 5.0
	if params.TimeoutS != nil {
		timeoutS = *params.TimeoutS
	}
	timeout := time.Duration(timeoutS * float64(time.Second))

	// Fast path: check if already routable
	if h.findRoute(params.Method) != nil {
		conn.Reply(ctx, req.ID, struct{}{})
		return
	}

	// Set up timeout
	deadline := time.Now().Add(timeout)
	timedOut := false

	// Start a goroutine that will broadcast when timeout expires
	// to wake up the waiting goroutine
	done := make(chan struct{})
	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			h.mu.Lock()
			timedOut = true
			h.ensureCond()
			h.routesCond.Broadcast()
			h.mu.Unlock()
		case <-done:
		case <-ctx.Done():
			h.mu.Lock()
			h.ensureCond()
			h.routesCond.Broadcast()
			h.mu.Unlock()
		}
	}()
	defer close(done)

	// Wait loop
	h.mu.Lock()
	h.ensureCond()
	for {
		// Check if routable (need to check under lock for consistency with Wait)
		if h.findRouteUnlocked(params.Method) != nil {
			h.mu.Unlock()
			conn.Reply(ctx, req.ID, struct{}{})
			return
		}

		// Check timeout
		if timedOut || time.Now().After(deadline) {
			h.mu.Unlock()
			conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
				Code:    CodeTimeout,
				Message: "timeout waiting for method to become routable: " + params.Method,
			})
			return
		}

		// Check context
		if ctx.Err() != nil {
			h.mu.Unlock()
			conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
				Code:    jsonrpc2.CodeInternalError,
				Message: "context cancelled",
			})
			return
		}

		h.routesCond.Wait()
	}
}

// findRouteUnlocked is like findRoute but assumes the lock is already held.
func (h *Handler) findRouteUnlocked(method string) *jsonrpc2.Conn {
	var bestPrefix string
	var bestConn *jsonrpc2.Conn

	for prefix, conn := range h.routes {
		if strings.HasPrefix(method, prefix) && len(prefix) > len(bestPrefix) {
			bestPrefix = prefix
			bestConn = conn
		}
	}

	return bestConn
}
