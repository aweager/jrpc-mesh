package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/aweager/jrpc-mesh/internal"
	"github.com/sourcegraph/jsonrpc2"
)

const defaultSocketPath = "/tmp/jrpc-mesh.sock"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	socketPath := defaultSocketPath
	if len(os.Args) > 1 {
		socketPath = os.Args[1]
	}

	// Remove existing socket file if it exists
	if err := os.RemoveAll(socketPath); err != nil {
		slog.Error("failed to remove existing socket", "error", err)
		os.Exit(1)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		slog.Error("failed to listen", "socket", socketPath, "error", err)
		os.Exit(1)
	}
	defer listener.Close()

	slog.Info("JSON RPC server listening", "socket", socketPath)

	// Handle graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		slog.Info("shutting down")
		cancel()
		listener.Close()
	}()

	handler := &internal.Handler{}

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				slog.Error("failed to accept connection", "error", err)
				continue
			}
		}

		go handleConnection(ctx, conn, handler)
	}
}

func handleConnection(ctx context.Context, conn net.Conn, handler *internal.Handler) {
	stream := jsonrpc2.NewBufferedStream(conn, internal.NewlineCodec{})
	rpcConn := jsonrpc2.NewConn(ctx, stream, handler)
	<-rpcConn.DisconnectNotify()
	handler.RemoveRoutesForConn(rpcConn)
}
