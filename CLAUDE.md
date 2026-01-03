# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

jrpc-mesh is a simple JSON RPC service mesh intended for local dev box services. Written in Go.

## Development Commands

*Note: This is a newly initialized project. Commands will be added as the project develops.*

```bash
# Initialize Go module (if not done)
go mod init github.com/aweager/jrpc-mesh

# Build
go build ./...

# Run tests
go test ./...

# Run a single test
go test -run TestName ./path/to/package

# Lint (if golangci-lint is configured)
golangci-lint run
```

## Architecture

*To be documented as the project develops.*
