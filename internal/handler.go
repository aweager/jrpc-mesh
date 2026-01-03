package internal

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/sourcegraph/jsonrpc2"
)

// Handler implements jsonrpc2.Handler for the jrpc-mesh proxy.
type Handler struct {
	mu     sync.RWMutex
	routes map[string]*jsonrpc2.Conn // prefix -> connection
}

// UpdateRoutesParams defines the parameters for awe.proxy/UpdateRoutes.
type UpdateRoutesParams struct {
	Prefixes []string `json:"prefixes"`
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
	if h.routes == nil {
		h.routes = make(map[string]*jsonrpc2.Conn)
	}
	for _, prefix := range params.Prefixes {
		h.routes[prefix] = conn
	}
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
