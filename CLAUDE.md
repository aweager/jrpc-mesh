# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

jrpc-mesh is a simple JSON RPC service mesh and reverse proxy intended for local dev box services. Written in Go.

## Development Commands

*Note: This is a newly initialized project. Commands will be added as the project develops.*

```bash
# Build
make

# Run tests
go test ./...

# Run a single test
go test -run TestName ./path/to/package

# Lint (if golangci-lint is configured)
golangci-lint run

# Format all markdown documentation
mdformat .

## Format a single markdown file
mdformat ./path/to/file.md
```

## Architecture

The reverse proxy listens on a unix domain socket. Services connect to the
socket and inform the proxy of their routes using the
`awe.proxy/UpdateRoutes` method. Any RPC whose method begins with `awe.proxy/`
will be handled by the proxy itself. All other messages are routed to the appropriate backend.
