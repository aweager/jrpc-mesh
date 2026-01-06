package internal

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/sourcegraph/jsonrpc2"
)

// Handler implements jsonrpc2.Handler for the jrpc-mesh proxy.
type Handler struct {
	Routes RouteTable

	peerMu sync.RWMutex
	peers  map[*jsonrpc2.Conn]bool // Track peer proxy connections
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
	case "AddPeerProxy":
		h.handleAddPeerProxy(ctx, conn, req)
	case "RegisterAsPeer":
		h.handleRegisterAsPeer(ctx, conn, req)
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

func (h *Handler) handleAddPeerProxy(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if req.Notif {
		return
	}

	var params AddPeerProxyParams
	if err := json.Unmarshal(*req.Params, &params); err != nil {
		conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInvalidParams,
			Message: "invalid params: " + err.Error(),
		})
		return
	}

	if params.Socket == "" {
		conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInvalidParams,
			Message: "socket is required",
		})
		return
	}

	// Connect to peer proxy socket
	peerNetConn, err := net.Dial("unix", params.Socket)
	if err != nil {
		conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInternalError,
			Message: "failed to connect to peer: " + err.Error(),
		})
		return
	}

	// Create JSON-RPC connection to peer
	// The peer's UpdateRoutes calls will be handled by this handler
	stream := jsonrpc2.NewBufferedStream(peerNetConn, NewlineCodec{})
	peerConn := jsonrpc2.NewConn(ctx, stream, jsonrpc2.AsyncHandler(h))

	// Register peer connection
	h.peerMu.Lock()
	if h.peers == nil {
		h.peers = make(map[*jsonrpc2.Conn]bool)
	}
	h.peers[peerConn] = true
	h.peerMu.Unlock()

	// Wire up route change notifications if not already done
	h.Routes.OnRoutesChanged = h.notifyPeers

	// Register ourselves as a peer with the remote proxy
	// This will make the peer send us route updates and return its current routes
	var peerRoutes RegisterAsPeerResult
	if err := peerConn.Call(ctx, "awe.proxy/RegisterAsPeer", nil, &peerRoutes); err != nil {
		// Clean up on failure
		h.peerMu.Lock()
		delete(h.peers, peerConn)
		h.peerMu.Unlock()
		peerConn.Close()
		conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInternalError,
			Message: "failed to register as peer: " + err.Error(),
		})
		return
	}

	// Store the peer's routes in our routing table
	h.Routes.Update(peerConn, peerRoutes.Prefixes)

	// Send our current local routes to peer (excluding peer-owned routes)
	h.sendRoutesToPeer(ctx, peerConn)

	// Handle peer disconnect in background
	go func() {
		<-peerConn.DisconnectNotify()
		h.peerMu.Lock()
		delete(h.peers, peerConn)
		h.peerMu.Unlock()
		h.Routes.RemoveConn(peerConn)
	}()

	conn.Reply(ctx, req.ID, struct{}{})
}

func (h *Handler) handleRegisterAsPeer(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if req.Notif {
		return
	}

	// Register this connection as a peer
	h.peerMu.Lock()
	if h.peers == nil {
		h.peers = make(map[*jsonrpc2.Conn]bool)
	}
	h.peers[conn] = true
	h.peerMu.Unlock()

	// Wire up route change notifications if not already done
	h.Routes.OnRoutesChanged = h.notifyPeers

	// Handle peer disconnect in background
	go func() {
		<-conn.DisconnectNotify()
		h.peerMu.Lock()
		delete(h.peers, conn)
		h.peerMu.Unlock()
		// Routes are cleaned up by the main connection handler
	}()

	// Get our current routes (excluding peer-owned routes)
	h.peerMu.RLock()
	exclude := make(map[*jsonrpc2.Conn]bool, len(h.peers))
	for p := range h.peers {
		exclude[p] = true
	}
	h.peerMu.RUnlock()

	prefixes := h.Routes.GetPrefixesExcluding(exclude)

	// Return our current routes
	conn.Reply(ctx, req.ID, RegisterAsPeerResult{
		Prefixes: prefixes,
	})
}

// notifyPeers sends current local routes to all connected peers.
// Called when routes change.
func (h *Handler) notifyPeers() {
	h.peerMu.RLock()
	peers := make([]*jsonrpc2.Conn, 0, len(h.peers))
	for peer := range h.peers {
		peers = append(peers, peer)
	}
	h.peerMu.RUnlock()

	for _, peer := range peers {
		h.sendRoutesToPeer(context.Background(), peer)
	}
}

// sendRoutesToPeer sends local routes to a specific peer, excluding routes owned by peers.
func (h *Handler) sendRoutesToPeer(ctx context.Context, peer *jsonrpc2.Conn) {
	// Get peers to exclude (don't send peer routes back to peers)
	h.peerMu.RLock()
	exclude := make(map[*jsonrpc2.Conn]bool, len(h.peers))
	for p := range h.peers {
		exclude[p] = true
	}
	h.peerMu.RUnlock()

	prefixes := h.Routes.GetPrefixesExcluding(exclude)

	// Send routes to peer (fire and forget - use Notify)
	peer.Notify(ctx, "awe.proxy/UpdateRoutes", UpdateRoutesParams{
		Prefixes: prefixes,
	})
}
