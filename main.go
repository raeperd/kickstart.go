package main

import (
	"context"
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

// Version is set at build time via ldflags (e.g., -X main.Version=v1.0.0).
var Version string

// run starts the [http.Server] and blocks until shutdown via OS signal.
// Dependencies are injected as parameters for testability.
// Inspired by https://grafana.com/blog/2024/02/09/how-i-write-http-services-in-go-after-13-years
func run(ctx context.Context, w io.Writer, getenv func(string) string, version string) error {
	port := uint64(8080)
	if p := getenv("PORT"); p != "" {
		var err error
		port, err = strconv.ParseUint(p, 10, 16)
		if err != nil || port == 0 {
			return fmt.Errorf("invalid PORT %q: port must be between 1 and 65535", p)
		}
	}

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Initialize resources here

	slog.SetDefault(slog.New(slog.NewJSONHandler(w, nil)))
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           route(slog.Default(), version),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errChan := make(chan error, 1)
	go func() {
		slog.InfoContext(ctx, "server started", slog.Uint64("port", port), slog.String("version", version))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- err
		}
	}()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		slog.InfoContext(ctx, "shutting down server", slog.Any("cause", context.Cause(ctx)))

		// Create a new context for shutdown with timeout
		ctx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("server shutdown: %w", err)
		}

		// Cleanup resources here, in reverse order of initialization
		return nil
	}
}

// route is the single source of truth for all endpoints, middleware, and their dependencies.
func route(log *slog.Logger, version string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth(version))
	mux.HandleFunc("/debug/", handleDebug())

	handler := accesslog(mux, log)
	handler = recovery(handler, log)
	return handler
}

// handleHealth responds with service health including version and VCS info.
func handleHealth(version string) http.HandlerFunc {
	type responseBody struct {
		Version        string    `json:"version"`
		Uptime         string    `json:"uptime"`
		LastCommitHash string    `json:"lastCommitHash"`
		LastCommitTime time.Time `json:"lastCommitTime"`
		DirtyBuild     bool      `json:"dirtyBuild"`
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

// handleDebug registers pprof and expvar routes under /debug/.
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

// accesslog logs request and response details.
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

// recovery recovers from panics. Must be outermost middleware to catch all panics.
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

// responseRecorder wraps [http.ResponseWriter] to record status and bytes written.
type responseRecorder struct {
	http.ResponseWriter
	status   int
	numBytes int
}

// Write records bytes and implicit 200 status.
func (re *responseRecorder) Write(b []byte) (int, error) {
	if re.status == 0 { // mirror net/http's implicit 200 on first Write
		re.status = http.StatusOK
	}
	re.numBytes += len(b)
	return re.ResponseWriter.Write(b)
}

// WriteHeader records the status code.
func (re *responseRecorder) WriteHeader(statusCode int) {
	re.status = statusCode
	re.ResponseWriter.WriteHeader(statusCode)
}
