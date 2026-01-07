package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sourcegraph/jsonrpc2"
)

const connectionRetry = time.Second * 5

var ErrNotConnected = errors.New("no current connection to mesh")

// Client represents a somewhat opinionated connection to the jrpc-mesh reverse proxy
type Client struct {
	mu       sync.RWMutex
	conn     *jsonrpc2.Conn
	netConn  net.Conn
	handlers map[string]Handler // service name -> handler

	notifyCh chan struct{}
	cancel   context.CancelFunc
}

func (c *Client) handleRequest(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (result any, err error) {
	var handler Handler
	var service string
	var method string
	for candidateService, candidateHandler := range c.handlers {
		candidateMethod, found := strings.CutPrefix(req.Method, candidateService+"/")
		if found {
			handler = candidateHandler
			service = candidateService
			method = candidateMethod
			break
		}
	}

	if handler == nil {
		return nil, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeMethodNotFound,
			Message: "Method not found",
		}
	}

	result, err = handler.Handle(ctx, service, method, req.Params)
	return result, ToJsonRpc2Error(err)
}

// Connect connects to the jrpc-mesh proxy and sets up the handler for incoming requests
func Connect(socketPath string) (*Client, error) {
	ctx, cancel := context.WithCancel(context.Background())

	client := &Client{
		notifyCh: make(chan struct{}, 1),
		handlers: make(map[string]Handler),
		cancel:   cancel,
	}

	go client.reconnectLoop(ctx, socketPath)
	go client.updateRoutesLoop(ctx)

	return client, nil
}

// RegisterService registers a new handler for incoming requests
func (c *Client) RegisterService(serviceName string, handler Handler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[serviceName] = handler
	select {
	case c.notifyCh <- struct{}{}:
	default:
	}
}

// Call makes a JSON-RPC call to a service via the proxy.
// The serviceName is prepended to the method to route to the correct instance.
func (c *Client) Call(ctx context.Context, serviceName, method string, params, result any) error {
	fullMethod := serviceName + "/" + method

	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()

	if conn == nil {
		return ErrNotConnected
	}

	return conn.Call(ctx, fullMethod, params, result)
}

// Notify sends a JSON-RPC notification to a service via the proxy.
// The serviceName is prepended to the method to route to the correct instance.
func (c *Client) Notify(ctx context.Context, serviceName, method string, params any) error {
	fullMethod := serviceName + "/" + method

	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()

	if conn == nil {
		return ErrNotConnected
	}

	return conn.Notify(ctx, fullMethod, params)
}

// CallRaw makes a JSON-RPC call returning raw JSON
func (c *Client) CallRaw(ctx context.Context, serviceName, method string, params any) (json.RawMessage, error) {
	var result json.RawMessage
	err := c.Call(ctx, serviceName, method, params, &result)
	return result, err
}

// Close closes the proxy connection
func (c *Client) Close() error {
	c.cancel()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	if c.netConn != nil {
		err := c.netConn.Close()
		c.netConn = nil
		return err
	}
	return nil
}

// reconnectLoop connects to the mesh at socketPath and retries until canceled.
func (c *Client) reconnectLoop(ctx context.Context, socketPath string) {
	logger := slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		netConn, err := net.Dial("unix", socketPath)
		if err != nil {
			slog.Error(
				"failed to connect to proxy, will retry",
				"socket", socketPath,
				"retryAfter", connectionRetry,
				"err", err,
			)

			select {
			case <-ctx.Done():
			case <-time.After(connectionRetry):
			}

			continue
		}

		stream := jsonrpc2.NewBufferedStream(netConn, NewlineCodec{})
		conn := jsonrpc2.NewConn(ctx, stream, jsonrpc2.HandlerWithError(c.handleRequest), jsonrpc2.LogMessages(logger))

		c.mu.Lock()
		c.netConn = netConn
		c.conn = conn
		c.mu.Unlock()

		select {
		case c.notifyCh <- struct{}{}:
		default:
		}

		<-conn.DisconnectNotify()

		c.mu.Lock()
		c.netConn = nil
		c.conn = nil
		c.mu.Unlock()
	}
}

// updateRoutesLoop shares method route registrations with the mesh
// when they are updated or a new connection is established.
func (c *Client) updateRoutesLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.notifyCh:
			c.mu.RLock()
			services := slices.Collect(maps.Keys(c.handlers))
			conn := c.conn
			c.mu.RUnlock()

			prefixes := make([]string, 0, len(services))
			for _, service := range services {
				prefixes = append(prefixes, service+"/")
			}

			if conn != nil {
				conn.Notify(ctx, "awe.proxy/UpdateRoutes", UpdateRoutesParams{
					Prefixes: prefixes,
				})
			}
		}
	}
}
