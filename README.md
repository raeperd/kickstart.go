# kickstart.go
[![.github/workflows/build.yaml](https://github.com/raeperd/kickstart.go/actions/workflows/build.yaml/badge.svg)](https://github.com/raeperd/kickstart.go/actions/workflows/build.yaml)  [![Go Report Card](https://goreportcard.com/badge/github.com/raeperd/kickstart.go)](https://goreportcard.com/report/github.com/raeperd/kickstart.go) [![Coverage Status](https://coveralls.io/repos/github/raeperd/kickstart.go/badge.svg?branch=main)](https://coveralls.io/github/raeperd/kickstart.go?branch=main) [![Mentioned in Awesome Go](https://awesome.re/mentioned-badge.svg)](https://github.com/avelino/awesome-go)

Minimalistic HTTP server template in Go — single file, only standard library.
**Not** a framework, but a starting point for building HTTP services.

## Try it

```console
$ git clone https://github.com/raeperd/kickstart.go.git && cd kickstart.go
$ make run
$ curl localhost:8080/health
{"version":"local","uptime":"1.23s","lastCommitHash":"abc1234","lastCommitTime":"2024-01-01T00:00:00Z","dirtyBuild":false}
```

## Features
- Graceful shutdown with `SIGINT`/`SIGTERM` signal handling
- Health endpoint with version, git revision, and uptime
- Debug endpoints (`pprof`, `expvar`) out of the box
- Structured access logging and panic recovery middleware
- Designed for testability — integration tests against a real server, no mocks

## Start your own project
- Use this template to create a new repository, or fork it
- Find and replace all `raeperd/kickstart.go` with your repository name

## Reference
- [GopherCon Korea 2024 Session](https://www.youtube.com/live/DEZsPOSzNM0?si=ioPPAAb5JnOnpAoc&t=5113) (in Korean) / [Slides](https://raeperd.dev/go2024) (in English)
- Inspired by [Mat Ryer](https://grafana.com/blog/2024/02/09/how-i-write-http-services-in-go-after-13-years), [earthboundkid](https://blog.carlana.net/post/2023/golang-git-hash-how-to/), and [kickstart.nvim](https://github.com/nvim-lua/kickstart.nvim)
