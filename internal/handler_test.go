package internal

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/sourcegraph/jsonrpc2"
)

func TestFindRoute_NoRoutes(t *testing.T) {
	h := &Handler{}
	if got := h.findRoute("foo/bar"); got != nil {
		t.Errorf("findRoute() = %v, want nil", got)
	}
}

func TestFindRoute_ExactMatch(t *testing.T) {
	conn := &jsonrpc2.Conn{}
	h := &Handler{
		routes: map[string]*jsonrpc2.Conn{
			"foo/": conn,
		},
	}
	if got := h.findRoute("foo/bar"); got != conn {
		t.Errorf("findRoute() = %v, want %v", got, conn)
	}
}

func TestFindRoute_LongestPrefixWins(t *testing.T) {
	shortConn := &jsonrpc2.Conn{}
	longConn := &jsonrpc2.Conn{}
	h := &Handler{
		routes: map[string]*jsonrpc2.Conn{
			"foo/":     shortConn,
			"foo/bar/": longConn,
		},
	}

	// Should match longer prefix
	if got := h.findRoute("foo/bar/baz"); got != longConn {
		t.Errorf("findRoute(foo/bar/baz) = %v, want longConn", got)
	}

	// Should match shorter prefix when longer doesn't match
	if got := h.findRoute("foo/qux"); got != shortConn {
		t.Errorf("findRoute(foo/qux) = %v, want shortConn", got)
	}
}

func TestFindRoute_NoMatch(t *testing.T) {
	conn := &jsonrpc2.Conn{}
	h := &Handler{
		routes: map[string]*jsonrpc2.Conn{
			"foo/": conn,
		},
	}
	if got := h.findRoute("bar/baz"); got != nil {
		t.Errorf("findRoute(bar/baz) = %v, want nil", got)
	}
}

// testConn creates a pair of connected jsonrpc2.Conn for testing.
// Returns (client, server) connections.
func testConn(t *testing.T, handler jsonrpc2.Handler) (*jsonrpc2.Conn, *jsonrpc2.Conn) {
	t.Helper()
	clientConn, serverConn := net.Pipe()

	ctx := context.Background()
	client := jsonrpc2.NewConn(
		ctx,
		jsonrpc2.NewBufferedStream(clientConn, NewlineCodec{}),
		jsonrpc2.HandlerWithError(func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
			return nil, nil
		}),
	)
	server := jsonrpc2.NewConn(
		ctx,
		jsonrpc2.NewBufferedStream(serverConn, NewlineCodec{}),
		handler,
	)

	t.Cleanup(func() {
		client.Close()
		server.Close()
	})

	return client, server
}

func TestUpdateRoutes_RegistersPrefixes(t *testing.T) {
	h := &Handler{}
	client, _ := testConn(t, h)

	var result struct{}
	err := client.Call(context.Background(), "awe.proxy/UpdateRoutes", map[string]any{
		"prefixes": []string{"svc1/", "svc2/"},
	}, &result)
	if err != nil {
		t.Fatalf("UpdateRoutes failed: %v", err)
	}

	// Verify routes were registered
	h.mu.RLock()
	defer h.mu.RUnlock()

	if len(h.routes) != 2 {
		t.Errorf("expected 2 routes, got %d", len(h.routes))
	}
	if _, ok := h.routes["svc1/"]; !ok {
		t.Error("svc1/ not registered")
	}
	if _, ok := h.routes["svc2/"]; !ok {
		t.Error("svc2/ not registered")
	}
}

func TestUpdateRoutes_InvalidParams(t *testing.T) {
	h := &Handler{}
	client, _ := testConn(t, h)

	var result struct{}
	err := client.Call(context.Background(), "awe.proxy/UpdateRoutes", "not an object", &result)
	if err == nil {
		t.Fatal("expected error for invalid params")
	}

	rpcErr, ok := err.(*jsonrpc2.Error)
	if !ok {
		t.Fatalf("expected jsonrpc2.Error, got %T", err)
	}
	if rpcErr.Code != jsonrpc2.CodeInvalidParams {
		t.Errorf("expected CodeInvalidParams, got %d", rpcErr.Code)
	}
}

func TestHandle_UnknownProxyMethod(t *testing.T) {
	h := &Handler{}
	client, _ := testConn(t, h)

	var result struct{}
	err := client.Call(context.Background(), "awe.proxy/UnknownMethod", nil, &result)
	if err == nil {
		t.Fatal("expected error for unknown proxy method")
	}

	rpcErr, ok := err.(*jsonrpc2.Error)
	if !ok {
		t.Fatalf("expected jsonrpc2.Error, got %T", err)
	}
	if rpcErr.Code != jsonrpc2.CodeMethodNotFound {
		t.Errorf("expected CodeMethodNotFound, got %d", rpcErr.Code)
	}
}

func TestHandle_NoBackendRegistered(t *testing.T) {
	h := &Handler{}
	client, _ := testConn(t, h)

	var result struct{}
	err := client.Call(context.Background(), "unknown/method", nil, &result)
	if err == nil {
		t.Fatal("expected error for unroutable method")
	}

	rpcErr, ok := err.(*jsonrpc2.Error)
	if !ok {
		t.Fatalf("expected jsonrpc2.Error, got %T", err)
	}
	if rpcErr.Code != jsonrpc2.CodeMethodNotFound {
		t.Errorf("expected CodeMethodNotFound, got %d", rpcErr.Code)
	}
}

func TestHandle_RoutesToBackend(t *testing.T) {
	h := &Handler{}

	// Create the proxy connection (client talks to proxy)
	proxyClient, _ := testConn(t, h)

	// Create a backend connection that will handle routed requests
	backendClientConn, backendServerConn := net.Pipe()
	ctx := context.Background()

	// Backend client (proxy's view of backend)
	backendClient := jsonrpc2.NewConn(
		ctx,
		jsonrpc2.NewBufferedStream(backendClientConn, NewlineCodec{}),
		jsonrpc2.HandlerWithError(func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
			return nil, nil
		}),
	)

	// Backend server (the actual backend service)
	backendServer := jsonrpc2.NewConn(
		ctx,
		jsonrpc2.NewBufferedStream(backendServerConn, NewlineCodec{}),
		jsonrpc2.HandlerWithError(func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
			if req.Method == "myservice/echo" {
				var params map[string]string
				if err := json.Unmarshal(*req.Params, &params); err != nil {
					return nil, err
				}
				return map[string]string{"echoed": params["msg"]}, nil
			}
			return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeMethodNotFound, Message: "not found"}
		}),
	)

	t.Cleanup(func() {
		backendClient.Close()
		backendServer.Close()
	})

	// Register the backend's routes
	h.mu.Lock()
	h.routes = map[string]*jsonrpc2.Conn{
		"myservice/": backendClient,
	}
	h.mu.Unlock()

	// Now call through the proxy
	var result map[string]string
	err := proxyClient.Call(context.Background(), "myservice/echo", map[string]string{"msg": "hello"}, &result)
	if err != nil {
		t.Fatalf("routed call failed: %v", err)
	}

	if result["echoed"] != "hello" {
		t.Errorf("expected echoed=hello, got %v", result)
	}
}

func TestRemoveRoutesForConn(t *testing.T) {
	conn1 := &jsonrpc2.Conn{}
	conn2 := &jsonrpc2.Conn{}

	h := &Handler{
		routes: map[string]*jsonrpc2.Conn{
			"svc1/":       conn1,
			"svc1/sub/":   conn1,
			"svc2/":       conn2,
			"other/":      conn2,
		},
	}

	// Remove routes for conn1
	h.RemoveRoutesForConn(conn1)

	h.mu.RLock()
	defer h.mu.RUnlock()

	// conn1 routes should be gone
	if _, ok := h.routes["svc1/"]; ok {
		t.Error("svc1/ should have been removed")
	}
	if _, ok := h.routes["svc1/sub/"]; ok {
		t.Error("svc1/sub/ should have been removed")
	}

	// conn2 routes should remain
	if _, ok := h.routes["svc2/"]; !ok {
		t.Error("svc2/ should still exist")
	}
	if _, ok := h.routes["other/"]; !ok {
		t.Error("other/ should still exist")
	}
}

func TestRemoveRoutesForConn_OnDisconnect(t *testing.T) {
	h := &Handler{}
	client, _ := testConn(t, h)

	// Register routes
	var result struct{}
	err := client.Call(context.Background(), "awe.proxy/UpdateRoutes", map[string]any{
		"prefixes": []string{"myservice/"},
	}, &result)
	if err != nil {
		t.Fatalf("UpdateRoutes failed: %v", err)
	}

	// Verify route exists
	h.mu.RLock()
	if len(h.routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(h.routes))
	}
	h.mu.RUnlock()

	// Close the client connection
	client.Close()

	// The testConn cleanup will close connections, but we need to simulate
	// what handleConnection does - call RemoveRoutesForConn
	// In a real scenario, this is called after DisconnectNotify returns

	// For this test, we directly verify RemoveRoutesForConn works
	// by getting the server-side connection and removing its routes
	h.mu.RLock()
	var serverConn *jsonrpc2.Conn
	for _, c := range h.routes {
		serverConn = c
		break
	}
	h.mu.RUnlock()

	if serverConn != nil {
		h.RemoveRoutesForConn(serverConn)
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.routes) != 0 {
		t.Errorf("expected 0 routes after cleanup, got %d", len(h.routes))
	}
}

// testService represents a service connected to the proxy for integration testing.
type testService struct {
	client *jsonrpc2.Conn // service's view - used to call other services via proxy
	server *jsonrpc2.Conn // proxy's view - receives routed calls from other services
}

// newTestService creates a service that connects to the proxy, registers routes,
// and handles incoming requests with the provided handler.
func newTestService(t *testing.T, h *Handler, prefixes []string, handler jsonrpc2.Handler) *testService {
	t.Helper()

	// Create pipe: serviceEnd <-> proxyEnd
	serviceEnd, proxyEnd := net.Pipe()
	ctx := context.Background()

	// Proxy's view of this connection - handles UpdateRoutes, routes calls
	proxyConn := jsonrpc2.NewConn(
		ctx,
		jsonrpc2.NewBufferedStream(proxyEnd, NewlineCodec{}),
		h,
	)

	// Service's view - handles incoming routed calls
	serviceConn := jsonrpc2.NewConn(
		ctx,
		jsonrpc2.NewBufferedStream(serviceEnd, NewlineCodec{}),
		handler,
	)

	t.Cleanup(func() {
		serviceConn.Close()
		proxyConn.Close()
	})

	// Register routes for this service
	if len(prefixes) > 0 {
		var result struct{}
		err := serviceConn.Call(ctx, "awe.proxy/UpdateRoutes", map[string]any{
			"prefixes": prefixes,
		}, &result)
		if err != nil {
			t.Fatalf("failed to register routes: %v", err)
		}
	}

	return &testService{
		client: serviceConn, // service uses this to call other services
		server: proxyConn,   // stored in routes, used for routing TO this service
	}
}

func TestIntegration_TwoServicesCallEachOther(t *testing.T) {
	h := &Handler{}

	// Create Service A - echoes with "A:" prefix
	serviceA := newTestService(t, h, []string{"serviceA/"}, jsonrpc2.HandlerWithError(
		func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
			if req.Method == "serviceA/echo" {
				var params map[string]string
				if err := json.Unmarshal(*req.Params, &params); err != nil {
					return nil, err
				}
				return map[string]string{"response": "A:" + params["msg"]}, nil
			}
			return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeMethodNotFound, Message: "not found"}
		},
	))

	// Create Service B - echoes with "B:" prefix
	serviceB := newTestService(t, h, []string{"serviceB/"}, jsonrpc2.HandlerWithError(
		func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
			if req.Method == "serviceB/echo" {
				var params map[string]string
				if err := json.Unmarshal(*req.Params, &params); err != nil {
					return nil, err
				}
				return map[string]string{"response": "B:" + params["msg"]}, nil
			}
			return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeMethodNotFound, Message: "not found"}
		},
	))

	// Service A calls Service B
	var resultB map[string]string
	err := serviceA.client.Call(context.Background(), "serviceB/echo", map[string]string{"msg": "hello from A"}, &resultB)
	if err != nil {
		t.Fatalf("A -> B call failed: %v", err)
	}
	if resultB["response"] != "B:hello from A" {
		t.Errorf("expected 'B:hello from A', got %q", resultB["response"])
	}

	// Service B calls Service A
	var resultA map[string]string
	err = serviceB.client.Call(context.Background(), "serviceA/echo", map[string]string{"msg": "hello from B"}, &resultA)
	if err != nil {
		t.Fatalf("B -> A call failed: %v", err)
	}
	if resultA["response"] != "A:hello from B" {
		t.Errorf("expected 'A:hello from B', got %q", resultA["response"])
	}
}

func TestIntegration_RouteCleanupBetweenServices(t *testing.T) {
	h := &Handler{}

	// Create Service A
	serviceA := newTestService(t, h, []string{"serviceA/"}, jsonrpc2.HandlerWithError(
		func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
			return map[string]string{"from": "A"}, nil
		},
	))

	// Create Service B
	serviceB := newTestService(t, h, []string{"serviceB/"}, jsonrpc2.HandlerWithError(
		func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
			return map[string]string{"from": "B"}, nil
		},
	))

	// Service A calls Service B - should work
	var result map[string]string
	err := serviceA.client.Call(context.Background(), "serviceB/method", nil, &result)
	if err != nil {
		t.Fatalf("initial call failed: %v", err)
	}

	// Close Service B and clean up its routes
	serviceB.server.Close()
	serviceB.client.Close()
	h.RemoveRoutesForConn(serviceB.server)

	// Service A tries to call Service B again - should fail with method not found
	err = serviceA.client.Call(context.Background(), "serviceB/method", nil, &result)
	if err == nil {
		t.Fatal("expected error after service B disconnected")
	}

	rpcErr, ok := err.(*jsonrpc2.Error)
	if !ok {
		t.Fatalf("expected jsonrpc2.Error, got %T", err)
	}
	if rpcErr.Code != jsonrpc2.CodeMethodNotFound {
		t.Errorf("expected CodeMethodNotFound, got %d", rpcErr.Code)
	}
}
