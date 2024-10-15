# kickstart.go
[![.github/workflows/build.yaml](https://github.com/raeperd/kickstart.go/actions/workflows/build.yaml/badge.svg)](https://github.com/raeperd/kickstart.go/actions/workflows/build.yaml) [![Go Report Card](https://goreportcard.com/badge/github.com/raeperd/kickstart.go)](https://goreportcard.com/report/github.com/raeperd/kickstart.go) [![codecov](https://codecov.io/gh/raeperd/kickstart.go/graph/badge.svg?token=T6jgDZXKVQ)](https://codecov.io/gh/raeperd/kickstart.go)   
Minimalistic http server template in go that is:
- Small (less than 300 lines of code)
- Single file 
- Only standard library dependencies

**Not** a framework, but a starting point for building HTTP services in Go.  

Inspired by [Mat Ryer](https://grafana.com/blog/2024/02/09/how-i-write-http-services-in-go-after-13-years) & [earthboundkid](https://blog.carlana.net/post/2023/golang-git-hash-how-to/) and even [kickstart.nvim](https://github.com/nvim-lua/kickstart.nvim)

## Features
- Graceful shutdown: Handles `SIGINT` and `SIGTERM` signals to shutdown gracefully.
- Health endpoint: Returns the server's health status including version and revision.
- OpenAPI endpoint: Serves an OpenAPI specification.
- Debug information: Provides various debug metrics including pprof and expvars.
- Access logging: Logs request details including latency, method, path, status, and bytes written.
- Panic recovery: Catch and log panics in HTTP handlers gracefully.
- Fully documented: Includes comments and documentation for all exported functions and types.

## Getting started
- Use this template to create a new repository
- Or fork the repository and make changes to suit your needs.

### Requirements
Go 1.22 or later

### Suggested Dependencies
- [golangci-lint](https://golangci-lint.run/) 
- [air](https://github.com/air-verse/air)

### Build and run the server
```sh
$ make run 
```
- this will build the server and run it on port 8080
- Checkout Makefile for more 

## Endpoints
- GET /health: Returns the health of the service, including version, revision, and modification status.
- GET /openapi.yaml: Returns the OpenAPI specification of the service.
- GET /debug/pprof: Returns the pprof debug information.
- GET /debug/vars: Returns the expvars debug information.

## How to 

### How to start a new project
- Use this template to create a new repository
- Or fork the repository and make changes to suit your needs.
- Find and replace all strings `raeperd/kickstart.go` with your repository/image name

### How to remove all comments from the code
```sh
$ sed -i '' '/^\/\/go:embed/! {/^\s*\/\/.*$/d; /^\s*\/\*\*/,/\*\//d;}' *.go
```
