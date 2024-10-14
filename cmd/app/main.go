package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"expvar"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"
)

func main() {
	ctx := context.Background()
	if err := run(ctx, os.Stdout, os.Args, Version); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(1)
	}
}

// Version is set at build time using ldflags.
// It is optional and can be omitted if not required.
// Refer to [handleGetHealth] for more information.
var Version string

// run initiates and starts the [http.Server], blocking until the context is canceled by OS signals.
// It listens on a port specified by the -port flag, defaulting to 8080.
// This function is inspired by techniques discussed in the [blog post] By Mat Ryer:
//
// [blog post]: https://grafana.com/blog/2024/02/09/how-i-write-http-services-in-go-after-13-years
func run(ctx context.Context, w io.Writer, args []string, version string) error {
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var port uint
	fs := flag.NewFlagSet(args[0], flag.ExitOnError)
	fs.SetOutput(w)
	fs.UintVar(&port, "port", 8080, "port for http api")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(w, nil)))
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: route(slog.Default(), version),
	}

	go func() {
		slog.InfoContext(ctx, "server started", slog.String("addr", server.Addr))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.ErrorContext(ctx, "server error", slog.Any("error", err))
		}
	}()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	return nil
}

// route sets up and returns an [http.Handler] for all the server routes.
// It is the single source of truth for all the routes.
// You can add custom [http.Handler] as needed.
func route(log *slog.Logger, version string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /health", handleGetHealth(version))
	mux.Handle("GET /openapi.yaml", handleGetOpenapi(version))
	mux.Handle("/debug/", handleGetDebug())

	handler := accesslog(mux, log)
	handler = recovery(handler, log)
	return handler
}

// handleGetHealth returns an [http.HandlerFunc] that responds with the health status of the service.
// It includes the service version, VCS revision, build time, and modified status.
// The service version can be set at build time using the VERSION variable (e.g., 'make build VERSION=v1.0.0').
func handleGetHealth(version string) http.HandlerFunc {
	type responseBody struct {
		Version        string    `json:"Version"`
		Uptime         string    `json:"Uptime"`
		LastCommitHash string    `json:"LastCommitHash"`
		LastCommitTime time.Time `json:"LastCommitTime"`
		DirtyBuild     bool      `json:"DirtyBuild"`
	}

	res := responseBody{Version: version}
	buildInfo, _ := debug.ReadBuildInfo()
	for _, kv := range buildInfo.Settings {
		if kv.Value == "" {
			continue
		}
		switch kv.Key {
		case "vcs.revision":
			res.LastCommitHash = kv.Value
		case "vcs.time":
			res.LastCommitTime, _ = time.Parse(time.RFC3339, kv.Value)
		case "vcs.modified":
			res.DirtyBuild = kv.Value == "true"
		}
	}

	up := time.Now()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		res.Uptime = time.Since(up).String()
		if err := json.NewEncoder(w).Encode(res); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// handleGetDebug returns an [http.Handler] for debug routes, including pprof and expvar routes.
func handleGetDebug() http.Handler {
	mux := http.NewServeMux()

	// NOTE: this route is same as defined in net/http/pprof init function
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// NOTE: this route is same as defined in expvar init function
	mux.Handle("/debug/vars", expvar.Handler())
	return mux
}

// handleGetOpenapi returns an [http.HandlerFunc] that serves the OpenAPI specification YAML file.
// The file is embedded in the binary using the go:embed directive.
func handleGetOpenapi(version string) http.HandlerFunc {
	body := bytes.Replace(openapi, []byte("${{ VERSION }}"), []byte(version), 1)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(body); err != nil {
			slog.ErrorContext(r.Context(), "failed to write openapi", slog.Any("error", err))
		}
	}
}

// openapi holds the embedded OpenAPI YAML file.
// Remove this and the api/openapi.yaml file if you prefer not to serve OpenAPI.
//
//go:embed openapi.yaml
var openapi []byte

// accesslog is a middleware that logs request and response details,
// including latency, method, path, query parameters, IP address, response status, and bytes sent.
func accesslog(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wr := responseRecorder{ResponseWriter: w}

		next.ServeHTTP(&wr, r)

		log.InfoContext(r.Context(), "accessed",
			slog.String("latency", time.Since(start).String()),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("query", r.URL.RawQuery),
			slog.String("ip", r.RemoteAddr),
			slog.Int("status", wr.status),
			slog.Int("bytes", wr.numBytes))
	})
}

// recovery is a middleware that recovers from panics during HTTP handler execution and logs the error details.
// It must be the last middleware in the chain to ensure it captures all panics.
func recovery(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wr := responseRecorder{ResponseWriter: w}
		defer func() {
			if err := recover(); err != nil {
				if err == http.ErrAbortHandler { // Handle the abort gracefully
					return
				}

				stack := make([]byte, 1024)
				n := runtime.Stack(stack, true)

				log.ErrorContext(r.Context(), "panic!",
					slog.Any("error", err),
					slog.String("stack", string(stack[:n])),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("query", r.URL.RawQuery),
					slog.String("ip", r.RemoteAddr))

				if wr.status == 0 { // response is not written yet
					http.Error(w, fmt.Sprintf("%v", err), http.StatusInternalServerError)
				}
			}
		}()
		next.ServeHTTP(&wr, r)
	})
}

// responseRecorder is a wrapper around [http.ResponseWriter] that records the status and bytes written during the response.
// It implements the [http.ResponseWriter] interface by embedding the original ResponseWriter.
type responseRecorder struct {
	http.ResponseWriter
	status   int
	numBytes int
}

// Header implements the [http.ResponseWriter] interface.
func (re *responseRecorder) Header() http.Header {
	return re.ResponseWriter.Header()
}

// Write implements the [http.ResponseWriter] interface.
func (re *responseRecorder) Write(b []byte) (int, error) {
	re.numBytes += len(b)
	return re.ResponseWriter.Write(b)
}

// WriteHeader implements the [http.ResponseWriter] interface.
func (re *responseRecorder) WriteHeader(statusCode int) {
	re.status = statusCode
	re.ResponseWriter.WriteHeader(statusCode)
}
