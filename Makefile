.PHONY: jrpc-mesh

all: jrpc-mesh

bin:
	mkdir -p bin

jrpc-mesh: bin
	go build -o bin/jrpc-mesh ./cmd/jrpc-mesh/jrpc-mesh.go
