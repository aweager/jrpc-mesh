package internal

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/aweager/jrpc-mesh/pkg/mesh"
	"github.com/sourcegraph/jsonrpc2"
)

// Handler implements jsonrpc2.Handler for the jrpc-mesh proxy.
type Handler struct {
	Routes *RouteTable

	peerMu      sync.RWMutex
	peers       map[*jsonrpc2.Conn]bool   // Track peer proxy connections
	peerSockets map[string]*jsonrpc2.Conn // Track peer connections by socket path
}

// NewHandler creates a new Handler with all necessary fields initialized.
func NewHandler() *Handler {
	h := &Handler{
		Routes:      NewRouteTable(),
		peers:       make(map[*jsonrpc2.Conn]bool),
		peerSockets: make(map[string]*jsonrpc2.Conn),
	}
	h.Routes.AddCallback(h.notifyPeers)
	return h
}

// HandleWithError processes incoming JSON RPC requests, returning the result or error.
func (h *Handler) HandleWithError(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
	// Handle proxy-specific methods
	if strings.HasPrefix(req.Method, "awe.proxy/") {
		return h.handleProxyMethod(ctx, conn, req)
	}

	// Route to appropriate backend service
	backend := h.Routes.Lookup(req.Method)
	if backend == nil {
		if req.Notif {
			return nil, nil
		}
		return nil, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeMethodNotFound,
			Message: "no backend registered for method: " + req.Method,
		}
	}

	if req.Notif {
		backend.Notify(ctx, req.Method, req.Params)
		return nil, nil
	}

	var result json.RawMessage
	if err := backend.Call(ctx, req.Method, req.Params, &result); err != nil {
		if rpcErr, ok := err.(*jsonrpc2.Error); ok {
			return nil, rpcErr
		}
		return nil, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInternalError,
			Message: err.Error(),
		}
	}
	return result, nil
}

func (h *Handler) handleProxyMethod(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
	method := strings.TrimPrefix(req.Method, "awe.proxy/")

	switch method {
	case "UpdateRoutes":
		return h.handleUpdateRoutes(ctx, conn, req)
	case "WaitUntilRoutable":
		return h.handleWaitUntilRoutable(ctx, conn, req)
	case "AddPeerProxy":
		return h.handleAddPeerProxy(ctx, conn, req)
	case "RegisterAsPeer":
		return h.handleRegisterAsPeer(ctx, conn, req)
	default:
		if req.Notif {
			return nil, nil
		}
		return nil, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeMethodNotFound,
			Message: "unknown proxy method: " + req.Method,
		}
	}
}

func (h *Handler) handleUpdateRoutes(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
	var params mesh.UpdateRoutesParams
	if err := json.Unmarshal(*req.Params, &params); err != nil {
		if req.Notif {
			return nil, nil
		}
		return nil, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInvalidParams,
			Message: "invalid params: " + err.Error(),
		}
	}

	h.Routes.Update(conn, params.Prefixes)

	if req.Notif {
		return nil, nil
	}
	return struct{}{}, nil
}

func (h *Handler) handleWaitUntilRoutable(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
	if req.Notif {
		return nil, nil
	}

	var params mesh.WaitUntilRoutableParams
	if err := json.Unmarshal(*req.Params, &params); err != nil {
		return nil, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInvalidParams,
			Message: "invalid params: " + err.Error(),
		}
	}

	if params.Method == "" {
		return nil, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInvalidParams,
			Message: "method is required",
		}
	}

	// Default timeout is 5.0 seconds
	timeoutS := 5.0
	if params.TimeoutS != nil {
		timeoutS = *params.TimeoutS
	}

	var waitCtx context.Context
	var cancel context.CancelFunc
	if timeoutS >= 0 {
		// Create a context with timeout
		timeout := time.Duration(timeoutS * float64(time.Second))
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		// Infinite timeout
		waitCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	err := h.Routes.WaitUntilRoutable(waitCtx, params.Method)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &jsonrpc2.Error{
				Code:    mesh.CodeTimeout,
				Message: "timeout waiting for method to become routable: " + params.Method,
			}
		}
		return nil, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInternalError,
			Message: err.Error(),
		}
	}

	return struct{}{}, nil
}

func (h *Handler) handleAddPeerProxy(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
	if req.Notif {
		return nil, nil
	}

	var params mesh.AddPeerProxyParams
	if err := json.Unmarshal(*req.Params, &params); err != nil {
		return nil, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInvalidParams,
			Message: "invalid params: " + err.Error(),
		}
	}

	if params.Socket == "" {
		return nil, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInvalidParams,
			Message: "socket is required",
		}
	}

	// Check if we already have a connection to this socket
	h.peerMu.Lock()
	if existingConn, exists := h.peerSockets[params.Socket]; exists {
		// Check if the connection is still alive
		select {
		case <-existingConn.DisconnectNotify():
			// Connection is dead, clean it up
			delete(h.peerSockets, params.Socket)
			delete(h.peers, existingConn)
		default:
			// Connection is still alive, return success (idempotent)
			h.peerMu.Unlock()
			return struct{}{}, nil
		}
	}
	h.peerMu.Unlock()

	// Connect to peer proxy socket
	peerNetConn, err := net.Dial("unix", params.Socket)
	if err != nil {
		return nil, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInternalError,
			Message: "failed to connect to peer: " + err.Error(),
		}
	}

	// Create JSON-RPC connection to peer
	// The peer's UpdateRoutes calls will be handled by this handler
	stream := jsonrpc2.NewBufferedStream(peerNetConn, mesh.NewlineCodec{})
	peerConn := jsonrpc2.NewConn(ctx, stream, jsonrpc2.AsyncHandler(jsonrpc2.HandlerWithError(h.HandleWithError)))

	// Register peer connection
	h.peerMu.Lock()
	if h.peers == nil {
		h.peers = make(map[*jsonrpc2.Conn]bool)
	}
	if h.peerSockets == nil {
		h.peerSockets = make(map[string]*jsonrpc2.Conn)
	}
	h.peers[peerConn] = true
	h.peerSockets[params.Socket] = peerConn
	h.peerMu.Unlock()

	// Register ourselves as a peer with the remote proxy
	// This will make the peer send us route updates and return its current routes
	var peerRoutes mesh.RegisterAsPeerResult
	if err := peerConn.Call(ctx, "awe.proxy/RegisterAsPeer", nil, &peerRoutes); err != nil {
		// Clean up on failure
		h.peerMu.Lock()
		delete(h.peers, peerConn)
		delete(h.peerSockets, params.Socket)
		h.peerMu.Unlock()
		peerConn.Close()
		return nil, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInternalError,
			Message: "failed to register as peer: " + err.Error(),
		}
	}

	// Store the peer's routes in our routing table
	h.Routes.Update(peerConn, peerRoutes.Prefixes)

	// Send our current local routes to peer (excluding peer-owned routes)
	h.sendRoutesToPeer(ctx, peerConn)

	// Handle peer disconnect in background
	socketPath := params.Socket // Capture for use in goroutine
	go func() {
		<-peerConn.DisconnectNotify()
		h.peerMu.Lock()
		delete(h.peers, peerConn)
		delete(h.peerSockets, socketPath)
		h.peerMu.Unlock()
		h.Routes.RemoveConn(peerConn)
	}()

	return struct{}{}, nil
}

func (h *Handler) handleRegisterAsPeer(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
	if req.Notif {
		return nil, nil
	}

	// Register this connection as a peer
	h.peerMu.Lock()
	if h.peers == nil {
		h.peers = make(map[*jsonrpc2.Conn]bool)
	}
	h.peers[conn] = true
	h.peerMu.Unlock()

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
	return mesh.RegisterAsPeerResult{
		Prefixes: prefixes,
	}, nil
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
	peer.Notify(ctx, "awe.proxy/UpdateRoutes", mesh.UpdateRoutesParams{
		Prefixes: prefixes,
	})
}
