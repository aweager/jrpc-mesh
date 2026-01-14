package internal

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/aweager/jrpc-mesh/pkg/mesh"
	"github.com/sourcegraph/jsonrpc2"
)

func TestLookup_NoRoutes(t *testing.T) {
	rt := NewRouteTable()
	if got := rt.Lookup("foo/bar"); got != nil {
		t.Errorf("Lookup() = %v, want nil", got)
	}
}

func TestLookup_ExactMatch(t *testing.T) {
	conn := &jsonrpc2.Conn{}
	rt := NewRouteTable()
	rt.routes["foo/"] = conn

	if got := rt.Lookup("foo/bar"); got != conn {
		t.Errorf("Lookup() = %v, want %v", got, conn)
	}
}

func TestLookup_LongestPrefixWins(t *testing.T) {
	shortConn := &jsonrpc2.Conn{}
	longConn := &jsonrpc2.Conn{}
	rt := NewRouteTable()
	rt.routes["foo/"] = shortConn
	rt.routes["foo/bar/"] = longConn

	// Should match longer prefix
	if got := rt.Lookup("foo/bar/baz"); got != longConn {
		t.Errorf("Lookup(foo/bar/baz) = %v, want longConn", got)
	}

	// Should match shorter prefix when longer doesn't match
	if got := rt.Lookup("foo/qux"); got != shortConn {
		t.Errorf("Lookup(foo/qux) = %v, want shortConn", got)
	}
}

func TestLookup_NoMatch(t *testing.T) {
	conn := &jsonrpc2.Conn{}
	rt := NewRouteTable()
	rt.routes["foo/"] = conn

	if got := rt.Lookup("bar/baz"); got != nil {
		t.Errorf("Lookup(bar/baz) = %v, want nil", got)
	}
}

// testConn creates a pair of connected jsonrpc2.Conn for testing.
// Returns (client, server) connections.
func testConn(t *testing.T, handler *Handler) (*jsonrpc2.Conn, *jsonrpc2.Conn) {
	t.Helper()
	clientConn, serverConn := net.Pipe()

	ctx := context.Background()
	client := jsonrpc2.NewConn(
		ctx,
		jsonrpc2.NewBufferedStream(clientConn, mesh.NewlineCodec{}),
		jsonrpc2.HandlerWithError(func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
			return nil, nil
		}),
	)
	server := jsonrpc2.NewConn(
		ctx,
		jsonrpc2.NewBufferedStream(serverConn, mesh.NewlineCodec{}),
		jsonrpc2.HandlerWithError(handler.HandleWithError),
	)

	t.Cleanup(func() {
		client.Close()
		server.Close()
	})

	return client, server
}

func TestUpdateRoutes_RegistersPrefixes(t *testing.T) {
	h := NewHandler()
	client, _ := testConn(t, h)

	var result struct{}
	err := client.Call(context.Background(), "awe.proxy/UpdateRoutes", map[string]any{
		"prefixes": []string{"svc1/", "svc2/"},
	}, &result)
	if err != nil {
		t.Fatalf("UpdateRoutes failed: %v", err)
	}

	// Verify routes were registered
	h.Routes.mu.RLock()
	defer h.Routes.mu.RUnlock()

	if len(h.Routes.routes) != 2 {
		t.Errorf("expected 2 routes, got %d", len(h.Routes.routes))
	}
	if _, ok := h.Routes.routes["svc1/"]; !ok {
		t.Error("svc1/ not registered")
	}
	if _, ok := h.Routes.routes["svc2/"]; !ok {
		t.Error("svc2/ not registered")
	}
}

func TestUpdateRoutes_InvalidParams(t *testing.T) {
	h := NewHandler()
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
	h := NewHandler()
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
	h := NewHandler()
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
	h := NewHandler()

	// Create the proxy connection (client talks to proxy)
	proxyClient, _ := testConn(t, h)

	// Create a backend connection that will handle routed requests
	backendClientConn, backendServerConn := net.Pipe()
	ctx := context.Background()

	// Backend client (proxy's view of backend)
	backendClient := jsonrpc2.NewConn(
		ctx,
		jsonrpc2.NewBufferedStream(backendClientConn, mesh.NewlineCodec{}),
		jsonrpc2.HandlerWithError(func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
			return nil, nil
		}),
	)

	// Backend server (the actual backend service)
	backendServer := jsonrpc2.NewConn(
		ctx,
		jsonrpc2.NewBufferedStream(backendServerConn, mesh.NewlineCodec{}),
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
	h.Routes.mu.Lock()
	h.Routes.routes = map[string]*jsonrpc2.Conn{
		"myservice/": backendClient,
	}
	h.Routes.mu.Unlock()

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

func TestRemoveConn(t *testing.T) {
	conn1 := &jsonrpc2.Conn{}
	conn2 := &jsonrpc2.Conn{}

	rt := NewRouteTable()
	rt.routes["svc1/"] = conn1
	rt.routes["svc1/sub/"] = conn1
	rt.routes["svc2/"] = conn2
	rt.routes["other/"] = conn2

	// Remove routes for conn1
	rt.RemoveConn(conn1)

	rt.mu.RLock()
	defer rt.mu.RUnlock()

	// conn1 routes should be gone
	if _, ok := rt.routes["svc1/"]; ok {
		t.Error("svc1/ should have been removed")
	}
	if _, ok := rt.routes["svc1/sub/"]; ok {
		t.Error("svc1/sub/ should have been removed")
	}

	// conn2 routes should remain
	if _, ok := rt.routes["svc2/"]; !ok {
		t.Error("svc2/ should still exist")
	}
	if _, ok := rt.routes["other/"]; !ok {
		t.Error("other/ should still exist")
	}
}

func TestRemoveConn_OnDisconnect(t *testing.T) {
	h := NewHandler()
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
	h.Routes.mu.RLock()
	if len(h.Routes.routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(h.Routes.routes))
	}
	h.Routes.mu.RUnlock()

	// Close the client connection
	client.Close()

	// The testConn cleanup will close connections, but we need to simulate
	// what handleConnection does - call RemoveConn
	// In a real scenario, this is called after DisconnectNotify returns

	// For this test, we directly verify RemoveConn works
	// by getting the server-side connection and removing its routes
	h.Routes.mu.RLock()
	var serverConn *jsonrpc2.Conn
	for _, c := range h.Routes.routes {
		serverConn = c
		break
	}
	h.Routes.mu.RUnlock()

	if serverConn != nil {
		h.Routes.RemoveConn(serverConn)
	}

	h.Routes.mu.RLock()
	defer h.Routes.mu.RUnlock()
	if len(h.Routes.routes) != 0 {
		t.Errorf("expected 0 routes after cleanup, got %d", len(h.Routes.routes))
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
		jsonrpc2.NewBufferedStream(proxyEnd, mesh.NewlineCodec{}),
		jsonrpc2.HandlerWithError(h.HandleWithError),
	)

	// Service's view - handles incoming routed calls
	serviceConn := jsonrpc2.NewConn(
		ctx,
		jsonrpc2.NewBufferedStream(serviceEnd, mesh.NewlineCodec{}),
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
	h := NewHandler()

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

func TestUpdateRoutes_RemovesOldPrefixes(t *testing.T) {
	h := NewHandler()
	client, server := testConn(t, h)

	// Register initial routes
	var result struct{}
	err := client.Call(context.Background(), "awe.proxy/UpdateRoutes", map[string]any{
		"prefixes": []string{"svc1/", "svc2/", "svc3/"},
	}, &result)
	if err != nil {
		t.Fatalf("UpdateRoutes failed: %v", err)
	}

	// Verify all 3 routes are registered
	h.Routes.mu.RLock()
	if len(h.Routes.routes) != 3 {
		t.Fatalf("expected 3 routes, got %d", len(h.Routes.routes))
	}
	h.Routes.mu.RUnlock()

	// Update routes - keep svc1/, add svc4/, remove svc2/ and svc3/
	err = client.Call(context.Background(), "awe.proxy/UpdateRoutes", map[string]any{
		"prefixes": []string{"svc1/", "svc4/"},
	}, &result)
	if err != nil {
		t.Fatalf("UpdateRoutes (second call) failed: %v", err)
	}

	// Verify the correct routes exist
	h.Routes.mu.RLock()
	defer h.Routes.mu.RUnlock()

	if len(h.Routes.routes) != 2 {
		t.Errorf("expected 2 routes, got %d", len(h.Routes.routes))
	}
	if _, ok := h.Routes.routes["svc1/"]; !ok {
		t.Error("svc1/ should still be registered")
	}
	if _, ok := h.Routes.routes["svc4/"]; !ok {
		t.Error("svc4/ should be registered")
	}
	if _, ok := h.Routes.routes["svc2/"]; ok {
		t.Error("svc2/ should have been removed")
	}
	if _, ok := h.Routes.routes["svc3/"]; ok {
		t.Error("svc3/ should have been removed")
	}

	// Verify the routes point to the correct connection (server-side conn)
	if h.Routes.routes["svc1/"] != server {
		t.Error("svc1/ should be registered to the server connection")
	}
	if h.Routes.routes["svc4/"] != server {
		t.Error("svc4/ should be registered to the server connection")
	}
}

func TestUpdateRoutes_DoesNotAffectOtherConnections(t *testing.T) {
	h := NewHandler()

	// Create two separate connections
	client1, server1 := testConn(t, h)
	client2, server2 := testConn(t, h)

	var result struct{}

	// Connection 1 registers some routes
	err := client1.Call(context.Background(), "awe.proxy/UpdateRoutes", map[string]any{
		"prefixes": []string{"conn1-svc1/", "conn1-svc2/"},
	}, &result)
	if err != nil {
		t.Fatalf("UpdateRoutes for conn1 failed: %v", err)
	}

	// Connection 2 registers some routes
	err = client2.Call(context.Background(), "awe.proxy/UpdateRoutes", map[string]any{
		"prefixes": []string{"conn2-svc1/", "conn2-svc2/"},
	}, &result)
	if err != nil {
		t.Fatalf("UpdateRoutes for conn2 failed: %v", err)
	}

	// Verify all 4 routes exist
	h.Routes.mu.RLock()
	if len(h.Routes.routes) != 4 {
		t.Fatalf("expected 4 routes, got %d", len(h.Routes.routes))
	}
	h.Routes.mu.RUnlock()

	// Connection 1 updates its routes (removing conn1-svc2/)
	err = client1.Call(context.Background(), "awe.proxy/UpdateRoutes", map[string]any{
		"prefixes": []string{"conn1-svc1/"},
	}, &result)
	if err != nil {
		t.Fatalf("UpdateRoutes update for conn1 failed: %v", err)
	}

	// Verify conn1's old route is gone but conn2's routes are untouched
	h.Routes.mu.RLock()
	defer h.Routes.mu.RUnlock()

	if len(h.Routes.routes) != 3 {
		t.Errorf("expected 3 routes, got %d", len(h.Routes.routes))
	}

	// Connection 1's routes
	if _, ok := h.Routes.routes["conn1-svc1/"]; !ok {
		t.Error("conn1-svc1/ should still be registered")
	}
	if _, ok := h.Routes.routes["conn1-svc2/"]; ok {
		t.Error("conn1-svc2/ should have been removed")
	}

	// Connection 2's routes should be unchanged
	if c, ok := h.Routes.routes["conn2-svc1/"]; !ok || c != server2 {
		t.Error("conn2-svc1/ should still be registered to server2")
	}
	if c, ok := h.Routes.routes["conn2-svc2/"]; !ok || c != server2 {
		t.Error("conn2-svc2/ should still be registered to server2")
	}

	// Verify ownership
	if h.Routes.routes["conn1-svc1/"] != server1 {
		t.Error("conn1-svc1/ should be registered to server1")
	}
}

func TestUpdateRoutes_EmptyPrefixesRemovesAll(t *testing.T) {
	h := NewHandler()
	client, _ := testConn(t, h)

	var result struct{}

	// Register some routes
	err := client.Call(context.Background(), "awe.proxy/UpdateRoutes", map[string]any{
		"prefixes": []string{"svc1/", "svc2/"},
	}, &result)
	if err != nil {
		t.Fatalf("UpdateRoutes failed: %v", err)
	}

	// Verify routes exist
	h.Routes.mu.RLock()
	if len(h.Routes.routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(h.Routes.routes))
	}
	h.Routes.mu.RUnlock()

	// Update with empty prefixes - should remove all routes for this connection
	err = client.Call(context.Background(), "awe.proxy/UpdateRoutes", map[string]any{
		"prefixes": []string{},
	}, &result)
	if err != nil {
		t.Fatalf("UpdateRoutes (empty) failed: %v", err)
	}

	// Verify all routes are gone
	h.Routes.mu.RLock()
	defer h.Routes.mu.RUnlock()

	if len(h.Routes.routes) != 0 {
		t.Errorf("expected 0 routes after empty update, got %d", len(h.Routes.routes))
	}
}

func TestIntegration_RouteCleanupBetweenServices(t *testing.T) {
	h := NewHandler()

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
	h.Routes.RemoveConn(serviceB.server)

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

func TestWaitUntilRoutable_AlreadyRoutable(t *testing.T) {
	h := NewHandler()

	// First, register a route
	client1, _ := testConn(t, h)
	var result struct{}
	err := client1.Call(context.Background(), "awe.proxy/UpdateRoutes", map[string]any{
		"prefixes": []string{"myservice/"},
	}, &result)
	if err != nil {
		t.Fatalf("UpdateRoutes failed: %v", err)
	}

	// Now another client waits for that method - should return immediately
	client2, _ := testConn(t, h)
	err = client2.Call(context.Background(), "awe.proxy/WaitUntilRoutable", map[string]any{
		"method":    "myservice/DoSomething",
		"timeout_s": 1.0,
	}, &result)
	if err != nil {
		t.Fatalf("WaitUntilRoutable failed: %v", err)
	}
}

func TestWaitUntilRoutable_WaitsForRoute(t *testing.T) {
	h := NewHandler()

	client1, _ := testConn(t, h)
	client2, _ := testConn(t, h)

	// Start waiting in a goroutine
	waitDone := make(chan error, 1)
	go func() {
		var result struct{}
		err := client1.Call(context.Background(), "awe.proxy/WaitUntilRoutable", map[string]any{
			"method":    "myservice/DoSomething",
			"timeout_s": 5.0,
		}, &result)
		waitDone <- err
	}()

	// Give it a moment to start waiting
	time.Sleep(50 * time.Millisecond)

	// Now register the route
	var result struct{}
	err := client2.Call(context.Background(), "awe.proxy/UpdateRoutes", map[string]any{
		"prefixes": []string{"myservice/"},
	}, &result)
	if err != nil {
		t.Fatalf("UpdateRoutes failed: %v", err)
	}

	// Wait should complete successfully
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("WaitUntilRoutable failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitUntilRoutable did not return after route was registered")
	}
}

func TestWaitUntilRoutable_Timeout(t *testing.T) {
	h := NewHandler()
	client, _ := testConn(t, h)

	var result struct{}
	err := client.Call(context.Background(), "awe.proxy/WaitUntilRoutable", map[string]any{
		"method":    "nonexistent/method",
		"timeout_s": 0.1, // 100ms timeout
	}, &result)

	if err == nil {
		t.Fatal("expected timeout error")
	}

	rpcErr, ok := err.(*jsonrpc2.Error)
	if !ok {
		t.Fatalf("expected jsonrpc2.Error, got %T", err)
	}
	if rpcErr.Code != mesh.CodeTimeout {
		t.Errorf("expected CodeTimeout (%d), got %d", mesh.CodeTimeout, rpcErr.Code)
	}
}

func TestWaitUntilRoutable_DefaultTimeout(t *testing.T) {
	h := NewHandler()
	client, _ := testConn(t, h)

	start := time.Now()

	var result struct{}
	err := client.Call(context.Background(), "awe.proxy/WaitUntilRoutable", map[string]any{
		"method": "nonexistent/method",
		// No timeout_s specified - should default to 5.0
	}, &result)

	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error")
	}

	// Should have taken approximately 5 seconds (allow some tolerance)
	if elapsed < 4*time.Second || elapsed > 6*time.Second {
		t.Errorf("expected ~5s timeout, got %v", elapsed)
	}

	rpcErr, ok := err.(*jsonrpc2.Error)
	if !ok {
		t.Fatalf("expected jsonrpc2.Error, got %T", err)
	}
	if rpcErr.Code != mesh.CodeTimeout {
		t.Errorf("expected CodeTimeout (%d), got %d", mesh.CodeTimeout, rpcErr.Code)
	}
}

func TestWaitUntilRoutable_InvalidParams(t *testing.T) {
	h := NewHandler()
	client, _ := testConn(t, h)

	var result struct{}

	// Test with invalid JSON
	err := client.Call(context.Background(), "awe.proxy/WaitUntilRoutable", "not an object", &result)
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

	// Test with missing method
	err = client.Call(context.Background(), "awe.proxy/WaitUntilRoutable", map[string]any{
		"timeout_s": 1.0,
	}, &result)
	if err == nil {
		t.Fatal("expected error for missing method")
	}
	rpcErr, ok = err.(*jsonrpc2.Error)
	if !ok {
		t.Fatalf("expected jsonrpc2.Error, got %T", err)
	}
	if rpcErr.Code != jsonrpc2.CodeInvalidParams {
		t.Errorf("expected CodeInvalidParams, got %d", rpcErr.Code)
	}
}

// testPeerProxy creates a proxy that listens on a Unix socket for peer connections.
// Returns the socket path, the handler, and a cleanup function.
func testPeerProxy(t *testing.T) (string, *Handler, func()) {
	t.Helper()

	// Create a temp socket file
	socketPath := t.TempDir() + "/peer.sock"

	handler := NewHandler()
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to listen on socket: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Track connections for cleanup
	var connMu sync.Mutex
	var connections []*jsonrpc2.Conn

	// Accept connections in background
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // Listener closed
			}
			stream := jsonrpc2.NewBufferedStream(conn, mesh.NewlineCodec{})
			rpcConn := jsonrpc2.NewConn(ctx, stream, jsonrpc2.AsyncHandler(jsonrpc2.HandlerWithError(handler.HandleWithError)))

			connMu.Lock()
			connections = append(connections, rpcConn)
			connMu.Unlock()

			go func() {
				<-rpcConn.DisconnectNotify()
				handler.Routes.RemoveConn(rpcConn)
			}()
		}
	}()

	cleanup := func() {
		cancel()
		listener.Close()
		// Close all active connections
		connMu.Lock()
		for _, c := range connections {
			c.Close()
		}
		connMu.Unlock()
	}

	return socketPath, handler, cleanup
}

func TestAddPeerProxy_InvalidParams(t *testing.T) {
	h := NewHandler()
	client, _ := testConn(t, h)

	var result struct{}

	// Test with invalid JSON
	err := client.Call(context.Background(), "awe.proxy/AddPeerProxy", "not an object", &result)
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

	// Test with missing socket
	err = client.Call(context.Background(), "awe.proxy/AddPeerProxy", map[string]any{}, &result)
	if err == nil {
		t.Fatal("expected error for missing socket")
	}
	rpcErr, ok = err.(*jsonrpc2.Error)
	if !ok {
		t.Fatalf("expected jsonrpc2.Error, got %T", err)
	}
	if rpcErr.Code != jsonrpc2.CodeInvalidParams {
		t.Errorf("expected CodeInvalidParams, got %d", rpcErr.Code)
	}
}

func TestAddPeerProxy_InvalidSocket(t *testing.T) {
	h := NewHandler()
	client, _ := testConn(t, h)

	var result struct{}
	err := client.Call(context.Background(), "awe.proxy/AddPeerProxy", map[string]any{
		"socket": "/nonexistent/socket.sock",
	}, &result)

	if err == nil {
		t.Fatal("expected error for invalid socket")
	}
	rpcErr, ok := err.(*jsonrpc2.Error)
	if !ok {
		t.Fatalf("expected jsonrpc2.Error, got %T", err)
	}
	if rpcErr.Code != jsonrpc2.CodeInternalError {
		t.Errorf("expected CodeInternalError, got %d", rpcErr.Code)
	}
}

func TestAddPeerProxy_ConnectsToPeer(t *testing.T) {
	// Set up peer proxy
	peerSocket, _, cleanup := testPeerProxy(t)
	defer cleanup()

	// Set up local proxy
	h := NewHandler()
	client, _ := testConn(t, h)

	// Connect to peer
	var result struct{}
	err := client.Call(context.Background(), "awe.proxy/AddPeerProxy", map[string]any{
		"socket": peerSocket,
	}, &result)
	if err != nil {
		t.Fatalf("AddPeerProxy failed: %v", err)
	}

	// Verify peer is registered
	h.peerMu.RLock()
	peerCount := len(h.peers)
	h.peerMu.RUnlock()

	if peerCount != 1 {
		t.Errorf("expected 1 peer, got %d", peerCount)
	}
}

func TestAddPeerProxy_SharesLocalRoutes(t *testing.T) {
	// Set up peer proxy
	peerSocket, peerHandler, cleanup := testPeerProxy(t)
	defer cleanup()

	// Set up local proxy with a service
	h := NewHandler()
	_ = newTestService(t, h, []string{"localService/"}, jsonrpc2.HandlerWithError(
		func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
			return map[string]string{"from": "local"}, nil
		},
	))

	// Connect to peer via another client
	client, _ := testConn(t, h)
	var result struct{}
	err := client.Call(context.Background(), "awe.proxy/AddPeerProxy", map[string]any{
		"socket": peerSocket,
	}, &result)
	if err != nil {
		t.Fatalf("AddPeerProxy failed: %v", err)
	}

	// Give time for routes to propagate
	time.Sleep(50 * time.Millisecond)

	// Verify peer received the routes
	peerHandler.Routes.mu.RLock()
	_, hasLocalRoute := peerHandler.Routes.routes["localService/"]
	peerHandler.Routes.mu.RUnlock()

	if !hasLocalRoute {
		t.Error("peer should have received localService/ route")
	}
}

func TestAddPeerProxy_ReceivesPeerRoutes(t *testing.T) {
	// Set up peer proxy with a service
	peerSocket, peerHandler, cleanup := testPeerProxy(t)
	defer cleanup()

	// Register a route on the peer before connecting
	peerService := newTestService(t, peerHandler, []string{"peerService/"}, jsonrpc2.HandlerWithError(
		func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
			return map[string]string{"from": "peer"}, nil
		},
	))

	// Set up local proxy
	h := NewHandler()
	client, _ := testConn(t, h)

	// Connect to peer
	var result struct{}
	err := client.Call(context.Background(), "awe.proxy/AddPeerProxy", map[string]any{
		"socket": peerSocket,
	}, &result)
	if err != nil {
		t.Fatalf("AddPeerProxy failed: %v", err)
	}

	// The peer needs to notify us of its routes
	// In this test setup, we simulate the peer sending UpdateRoutes
	// by having the peer's service call UpdateRoutes again
	err = peerService.client.Call(context.Background(), "awe.proxy/UpdateRoutes", map[string]any{
		"prefixes": []string{"peerService/"},
	}, &result)
	if err != nil {
		t.Fatalf("peer UpdateRoutes failed: %v", err)
	}

	// Give time for routes to propagate
	time.Sleep(50 * time.Millisecond)

	// Verify local proxy received the peer's routes
	h.Routes.mu.RLock()
	_, hasPeerRoute := h.Routes.routes["peerService/"]
	h.Routes.mu.RUnlock()

	if !hasPeerRoute {
		t.Error("local proxy should have received peerService/ route from peer")
	}
}

func TestAddPeerProxy_PropagatesRouteChanges(t *testing.T) {
	// Set up peer proxy
	peerSocket, peerHandler, cleanup := testPeerProxy(t)
	defer cleanup()

	// Set up local proxy
	h := NewHandler()
	client, _ := testConn(t, h)

	// Connect to peer first
	var result struct{}
	err := client.Call(context.Background(), "awe.proxy/AddPeerProxy", map[string]any{
		"socket": peerSocket,
	}, &result)
	if err != nil {
		t.Fatalf("AddPeerProxy failed: %v", err)
	}

	// Now add a local service
	_ = newTestService(t, h, []string{"newService/"}, jsonrpc2.HandlerWithError(
		func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
			return map[string]string{"from": "new"}, nil
		},
	))

	// Give time for routes to propagate
	time.Sleep(50 * time.Millisecond)

	// Verify peer received the new route
	peerHandler.Routes.mu.RLock()
	_, hasNewRoute := peerHandler.Routes.routes["newService/"]
	peerHandler.Routes.mu.RUnlock()

	if !hasNewRoute {
		t.Error("peer should have received newService/ route after it was added")
	}
}

func TestAddPeerProxy_CleanupOnDisconnect(t *testing.T) {
	// Set up peer proxy
	peerSocket, peerHandler, cleanup := testPeerProxy(t)

	// Set up local proxy
	h := NewHandler()
	client, _ := testConn(t, h)

	// Register a local service
	_ = newTestService(t, h, []string{"localService/"}, jsonrpc2.HandlerWithError(
		func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
			return map[string]string{"from": "local"}, nil
		},
	))

	// Connect to peer
	var result struct{}
	err := client.Call(context.Background(), "awe.proxy/AddPeerProxy", map[string]any{
		"socket": peerSocket,
	}, &result)
	if err != nil {
		t.Fatalf("AddPeerProxy failed: %v", err)
	}

	// Give time for routes to propagate
	time.Sleep(50 * time.Millisecond)

	// Verify peer received the route
	peerHandler.Routes.mu.RLock()
	routeCountBefore := len(peerHandler.Routes.routes)
	peerHandler.Routes.mu.RUnlock()

	if routeCountBefore == 0 {
		t.Fatal("peer should have received routes")
	}

	// Close the peer proxy
	cleanup()

	// Give time for disconnect to be processed
	time.Sleep(50 * time.Millisecond)

	// Verify peer is removed from local proxy's peer list
	h.peerMu.RLock()
	peerCount := len(h.peers)
	h.peerMu.RUnlock()

	if peerCount != 0 {
		t.Errorf("expected 0 peers after disconnect, got %d", peerCount)
	}
}

func TestAddPeerProxy_Idempotent(t *testing.T) {
	// Set up peer proxy
	peerSocket, _, cleanup := testPeerProxy(t)
	defer cleanup()

	// Set up local proxy
	h := NewHandler()
	client, _ := testConn(t, h)

	// Call AddPeerProxy first time
	var result struct{}
	err := client.Call(context.Background(), "awe.proxy/AddPeerProxy", map[string]any{
		"socket": peerSocket,
	}, &result)
	if err != nil {
		t.Fatalf("AddPeerProxy (first call) failed: %v", err)
	}

	// Verify peer is registered
	h.peerMu.RLock()
	peerCountFirst := len(h.peers)
	socketCountFirst := len(h.peerSockets)
	h.peerMu.RUnlock()

	if peerCountFirst != 1 {
		t.Errorf("expected 1 peer after first call, got %d", peerCountFirst)
	}
	if socketCountFirst != 1 {
		t.Errorf("expected 1 socket tracked after first call, got %d", socketCountFirst)
	}

	// Call AddPeerProxy second time with same socket (should be idempotent)
	err = client.Call(context.Background(), "awe.proxy/AddPeerProxy", map[string]any{
		"socket": peerSocket,
	}, &result)
	if err != nil {
		t.Fatalf("AddPeerProxy (second call) failed: %v", err)
	}

	// Verify we still have exactly one peer (not two)
	h.peerMu.RLock()
	peerCountSecond := len(h.peers)
	socketCountSecond := len(h.peerSockets)
	h.peerMu.RUnlock()

	if peerCountSecond != 1 {
		t.Errorf("expected 1 peer after second call (idempotent), got %d", peerCountSecond)
	}
	if socketCountSecond != 1 {
		t.Errorf("expected 1 socket tracked after second call (idempotent), got %d", socketCountSecond)
	}

	// Call AddPeerProxy third time (verify multiple calls work)
	err = client.Call(context.Background(), "awe.proxy/AddPeerProxy", map[string]any{
		"socket": peerSocket,
	}, &result)
	if err != nil {
		t.Fatalf("AddPeerProxy (third call) failed: %v", err)
	}

	// Verify we still have exactly one peer
	h.peerMu.RLock()
	peerCountThird := len(h.peers)
	socketCountThird := len(h.peerSockets)
	h.peerMu.RUnlock()

	if peerCountThird != 1 {
		t.Errorf("expected 1 peer after third call (idempotent), got %d", peerCountThird)
	}
	if socketCountThird != 1 {
		t.Errorf("expected 1 socket tracked after third call (idempotent), got %d", socketCountThird)
	}
}
