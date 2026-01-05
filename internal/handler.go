package internal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sourcegraph/jsonrpc2"
)

// CodeTimeout is the application-level error code for timeout errors.
const CodeTimeout = 1

// Handler implements jsonrpc2.Handler for the jrpc-mesh proxy.
type Handler struct {
	Routes RouteTable
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

// Handle processes incoming JSON RPC requests.
func (h *Handler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	// Handle proxy-specific methods
	if strings.HasPrefix(req.Method, "awe.proxy/") {
		h.handleProxyMethod(ctx, conn, req)
		return
	}

	// Route to appropriate backend service
	backend := h.Routes.Lookup(req.Method)
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

	h.Routes.Update(conn, params.Prefixes)

	if !req.Notif {
		conn.Reply(ctx, req.ID, struct{}{})
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

	err := h.Routes.WaitUntilRoutable(ctx, params.Method, timeout)
	if err != nil {
		if errors.Is(err, ErrWaitTimeout) {
			conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
				Code:    CodeTimeout,
				Message: "timeout waiting for method to become routable: " + params.Method,
			})
		} else {
			conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
				Code:    jsonrpc2.CodeInternalError,
				Message: err.Error(),
			})
		}
		return
	}

	conn.Reply(ctx, req.ID, struct{}{})
}
