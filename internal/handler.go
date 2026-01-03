package internal

import (
	"context"
	"strings"

	"github.com/sourcegraph/jsonrpc2"
)

// Handler implements jsonrpc2.Handler for the jrpc-mesh proxy.
type Handler struct{}

// Handle processes incoming JSON RPC requests.
func (h *Handler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	// Handle proxy-specific methods
	if strings.HasPrefix(req.Method, "awe.proxy/") {
		h.handleProxyMethod(ctx, conn, req)
		return
	}

	// TODO: Route to appropriate backend service
	if req.Notif {
		return
	}

	conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
		Code:    jsonrpc2.CodeMethodNotFound,
		Message: "no backend registered for method: " + req.Method,
	})
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
	// TODO: Implement route registration
	if req.Notif {
		return
	}
	conn.Reply(ctx, req.ID, map[string]string{"status": "ok"})
}
