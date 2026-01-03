package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/sourcegraph/jsonrpc2"
)

const defaultSocketPath = "/tmp/jrpc-mesh.sock"

func main() {
	socketPath := defaultSocketPath
	if len(os.Args) > 1 {
		socketPath = os.Args[1]
	}

	// Remove existing socket file if it exists
	if err := os.RemoveAll(socketPath); err != nil {
		log.Fatalf("Failed to remove existing socket: %v", err)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", socketPath, err)
	}
	defer listener.Close()

	log.Printf("JSON RPC server listening on %s", socketPath)

	// Handle graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Shutting down...")
		cancel()
		listener.Close()
	}()

	handler := &Handler{}

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("Failed to accept connection: %v", err)
				continue
			}
		}

		go handleConnection(ctx, conn, handler)
	}
}

func handleConnection(ctx context.Context, conn net.Conn, handler jsonrpc2.Handler) {
	stream := jsonrpc2.NewBufferedStream(conn, jsonrpc2.VSCodeObjectCodec{})
	rpcConn := jsonrpc2.NewConn(ctx, stream, handler)
	<-rpcConn.DisconnectNotify()
}
