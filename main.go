package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if err := run(context.Background(), os.Stdout, os.Getenv, Version); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(1)
	}
}

// Version is set at build time using ldflags.
// It is optional and can be omitted if not required.
// Refer to [handleHealth] for more information.
var Version string

// run initiates and starts the [http.Server], blocking until the context is canceled by OS signals.
// It listens on a port specified by the PORT environment variable, defaulting to 8080.
// This function is inspired by techniques discussed in the [blog post] By Mat Ryer:
//
// [blog post]: https://grafana.com/blog/2024/02/09/how-i-write-http-services-in-go-after-13-years
func run(ctx context.Context, w io.Writer, getenv func(string) string, version string) error {
	var port uint16 = 8080
	if p := getenv("PORT"); p != "" {
		v, err := strconv.ParseUint(p, 10, 16)
		if err != nil || v == 0 {
			return fmt.Errorf("invalid PORT %q: port must be between 1 and 65535", p)
		}
		port = uint16(v)
	}

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)

	// NOTE: Removed `defer cancel()` since we want to control when to cancel the context
	// We'll call it explicitly after server shutdown

	// Initialize your resources here, for example:
	// - Database connections
	// - Message queue clients
	// - Cache clients
	// - External API clients
	// Example:
	// db, err := sql.Open(...)
	// if err != nil {
	//     return fmt.Errorf("database init: %w", err)
	// }

	slog.SetDefault(slog.New(slog.NewJSONHandler(w, nil)))
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           route(slog.Default(), version),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errChan := make(chan error, 1)
	go func() {
		slog.InfoContext(ctx, "server started", slog.Uint64("port", uint64(port)), slog.String("version", version))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- err
		}
	}()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		slog.InfoContext(ctx, "shutting down server")

		// Create a new context for shutdown with timeout
		ctx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		// Shutdown the HTTP server first
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("server shutdown: %w", err)
		}

		// After server is shutdown, cancel the main context to close other resources
		cancel()

		// Add cleanup code here, in reverse order of initialization
		// Give each cleanup operation its own timeout if needed

		// Example cleanup sequence:
		// 1. Close application services that depend on other resources
		// if err := myService.Shutdown(ctx); err != nil {
		//     return fmt.Errorf("service shutdown: %w", err)
		// }

		// 2. Close message queue connections
		// if err := mqClient.Close(); err != nil {
		//     return fmt.Errorf("mq shutdown: %w", err)
		// }

		// 3. Close cache connections
		// if err := cacheClient.Close(); err != nil {
		//     return fmt.Errorf("cache shutdown: %w", err)
		// }

		// 4. Close database connections
		// if err := db.Close(); err != nil {
		//     return fmt.Errorf("database shutdown: %w", err)
		// }
		return nil
	}
}

// route sets up and returns an [http.Handler] for all the server routes.
// It is the single source of truth for all the routes.
// You can add custom [http.Handler] as needed.
func route(log *slog.Logger, version string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth(version))
	mux.HandleFunc("GET /openapi.yaml", handleOpenAPI(version))
	mux.HandleFunc("/debug/", handleDebug())

	handler := accesslog(mux, log)
	handler = recovery(handler, log)
	return handler
}

// handleHealth returns an [http.HandlerFunc] that responds with the health status of the service.
// It includes the service version, VCS revision, build time, and modified status.
// The service version can be set at build time using the VERSION variable (e.g., 'make build VERSION=v1.0.0').
func handleHealth(version string) http.HandlerFunc {
	type responseBody struct {
		Version        string    `json:"Version"`
		Uptime         string    `json:"Uptime"`
		LastCommitHash string    `json:"LastCommitHash"`
		LastCommitTime time.Time `json:"LastCommitTime"`
		DirtyBuild     bool      `json:"DirtyBuild"`
	}

	baseRes := responseBody{Version: version}
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range buildInfo.Settings {
			if kv.Value == "" {
				continue
			}
			switch kv.Key {
			case "vcs.revision":
				baseRes.LastCommitHash = kv.Value
			case "vcs.time":
				baseRes.LastCommitTime, _ = time.Parse(time.RFC3339, kv.Value)
			case "vcs.modified":
				baseRes.DirtyBuild = kv.Value == "true"
			}
		}
	}

	up := time.Now()
	return func(w http.ResponseWriter, _ *http.Request) {
		res := baseRes // Create a copy for each request to avoid data race
		res.Uptime = time.Since(up).String()

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(res); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// handleDebug returns an [http.HandlerFunc] for debug routes, including pprof and expvar routes.
func handleDebug() http.HandlerFunc {
	mux := http.NewServeMux()

	// NOTE: this route is same as defined in net/http/pprof init function
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// NOTE: this route is same as defined in expvar init function
	mux.Handle("/debug/vars", expvar.Handler())
	return mux.ServeHTTP
}

// handleOpenAPI returns an [http.HandlerFunc] that serves the OpenAPI specification YAML file.
// The file is embedded in the binary using the go:embed directive.
func handleOpenAPI(version string) http.HandlerFunc {
	body := bytes.Replace(openAPI, []byte("${{ VERSION }}"), []byte(version), 1)
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}

// openAPI holds the embedded OpenAPI YAML file.
// Remove this and the api/openapi.yaml file if you prefer not to serve OpenAPI.
//
//go:embed api/openapi.yaml
var openAPI []byte

// accesslog is a middleware that logs request and response details,
// including latency, method, path, query parameters, IP address, response status, and bytes sent.
func accesslog(next http.Handler, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
	}
}

// recovery is a middleware that recovers from panics during HTTP handler execution and logs the error details.
// It must be the last middleware in the chain to ensure it captures all panics.
func recovery(next http.Handler, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wr := responseRecorder{ResponseWriter: w}
		defer func() {
			err := recover()
			if err == nil {
				return
			}

			if err, ok := err.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				// Handle the abort gracefully
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

			if wr.status > 0 {
				// response was already sent, nothing we can do
				return
			}

			// send error response
			http.Error(w, fmt.Sprint(err), http.StatusInternalServerError)
		}()
		next.ServeHTTP(&wr, r)
	}
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
