# kickstart.go
[![.github/workflows/build.yaml](https://github.com/raeperd/kickstart.go/actions/workflows/build.yaml/badge.svg)](https://github.com/raeperd/kickstart.go/actions/workflows/build.yaml)  [![Go Report Card](https://goreportcard.com/badge/github.com/raeperd/kickstart.go)](https://goreportcard.com/report/github.com/raeperd/kickstart.go) [![Coverage Status](https://coveralls.io/repos/github/raeperd/kickstart.go/badge.svg?branch=main)](https://coveralls.io/github/raeperd/kickstart.go?branch=main) [![Go Reference](https://pkg.go.dev/badge/github.com/raeperd/kickstart.go.svg)](https://pkg.go.dev/github.com/raeperd/kickstart.go) [![Mentioned in Awesome Go](https://awesome.re/mentioned-badge.svg)](https://github.com/avelino/awesome-go)
Minimalistic http server template in go that is:
- Small (less than 300 lines of code)
- Single file
- Only standard library dependencies

**Not** a framework, but a starting point for building HTTP services in Go.
This project was first introduced in Gophercon Korea 2024, See [session in this link](https://www.youtube.com/live/DEZsPOSzNM0?si=ioPPAAb5JnOnpAoc&t=5113)(in Korean) and see [presentation in this link](https://raeperd.dev/go2024)(in english)

Inspired by [Mat Ryer](https://grafana.com/blog/2024/02/09/how-i-write-http-services-in-go-after-13-years) & [earthboundkid](https://blog.carlana.net/post/2023/golang-git-hash-how-to/) and even [kickstart.nvim](https://github.com/nvim-lua/kickstart.nvim)

## Features
- Graceful shutdown: Handles `SIGINT` and `SIGTERM` signals to shutdown gracefully.
- Health endpoint: Returns the server's health status including version and revision.
- OpenAPI endpoint: Serves an OpenAPI specification. using `embed` package
- Debug information: Provides various debug metrics including `pprof` and `expvars`.
- Access logging: Logs http request details using `slog`.
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

## Reference
- [Gophercon Korean 2024 Session](https://www.youtube.com/live/DEZsPOSzNM0?si=ioPPAAb5JnOnpAoc&t=5113) (in Korean)
- [How I write HTTP services in Go after 13 years | Grafana Labs](https://grafana.com/blog/2024/02/09/how-i-write-http-services-in-go-after-13-years/)
