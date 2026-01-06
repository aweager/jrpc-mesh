package mesh

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"

	"github.com/sourcegraph/jsonrpc2"
)

// Client represents a connection to the jrpc-mesh reverse proxy
type Client struct {
	conn    *jsonrpc2.Conn
	netConn net.Conn
}

// Connect connects to the jrpc-mesh proxy and sets up the handler for incoming requests
func Connect(ctx context.Context, socketPath string, handler jsonrpc2.Handler) (*Client, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to proxy at %s: %w", socketPath, err)
	}

	logger := slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug)

	stream := jsonrpc2.NewBufferedStream(conn, NewlineCodec{})
	rpcConn := jsonrpc2.NewConn(ctx, stream, handler, jsonrpc2.LogMessages(logger))

	return &Client{
		conn:    rpcConn,
		netConn: conn,
	}, nil
}

// UpdateRoutes registers method prefixes with the proxy
func (c *Client) UpdateRoutes(ctx context.Context, prefixes []string) error {
	return c.conn.Call(ctx, "awe.proxy/UpdateRoutes", UpdateRoutesParams{
		Prefixes: prefixes,
	}, nil)
}

// Conn returns the underlying JSON-RPC connection for making calls
func (c *Client) Conn() *jsonrpc2.Conn {
	return c.conn
}

// Call makes a JSON-RPC call to a service via the proxy.
// The serviceName is prepended to the method to route to the correct instance.
func (c *Client) Call(ctx context.Context, serviceName, method string, params, result any) error {
	fullMethod := serviceName + "/" + method
	return c.conn.Call(ctx, fullMethod, params, result)
}

// Notify sends a JSON-RPC notification to a service via the proxy.
// The serviceName is prepended to the method to route to the correct instance.
func (c *Client) Notify(ctx context.Context, serviceName, method string, params any) error {
	fullMethod := serviceName + "/" + method
	return c.conn.Notify(ctx, fullMethod, params)
}

// CallRaw makes a JSON-RPC call returning raw JSON
func (c *Client) CallRaw(ctx context.Context, serviceName, method string, params interface{}) (json.RawMessage, error) {
	var result json.RawMessage
	err := c.Call(ctx, serviceName, method, params, &result)
	return result, err
}

// Close closes the proxy connection
func (c *Client) Close() error {
	if c.conn != nil {
		c.conn.Close()
	}
	if c.netConn != nil {
		return c.netConn.Close()
	}
	return nil
}
