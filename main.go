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
		Handler:           newRootHTTPHandler(slog.Default(), version),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
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

// newRootHTTPHandler is the single source of truth for all endpoints, middleware, and their dependencies.
func newRootHTTPHandler(log *slog.Logger, version string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth(version))
	mux.HandleFunc("/debug/", handleDebug())

	handler := recovery(mux, log)
	return accesslog(handler, log)
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

// accesslog logs the final response, including recovered panics and aborted requests.
// Place it outside recovery so it observes the response recovery writes.
func accesslog(next http.Handler, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wr := responseRecorder{ResponseWriter: w, protoMajor: r.ProtoMajor}
		completed := false
		defer func() {
			if completed && !wr.committed() {
				wr.status = http.StatusOK // net/http sends 200 when the handler returns without a final header.
			}
			log.InfoContext(r.Context(), "accessed",
				slog.String("latency", time.Since(start).String()),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("query", r.URL.RawQuery),
				slog.String("ip", r.RemoteAddr),
				slog.Int("status", wr.status),
				slog.Int("bytes", wr.numBytes),
				slog.Bool("aborted", !completed))
		}()
		next.ServeHTTP(&wr, r)
		completed = true
	}
}

// recovery sends a generic 500 before a final response is committed, and aborts
// the connection or stream if a panic interrupts an already committed response.
func recovery(next http.Handler, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wr := responseRecorder{ResponseWriter: w, protoMajor: r.ProtoMajor}
		defer func() {
			err := recover()
			if err == nil {
				return
			}
			if err == http.ErrAbortHandler {
				panic(http.ErrAbortHandler) // Let net/http close the connection or reset the HTTP/2 stream.
			}
			log.ErrorContext(r.Context(), "panic!",
				slog.Any("error", err),
				slog.String("stack", string(debug.Stack())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("query", r.URL.RawQuery),
				slog.String("ip", r.RemoteAddr))
			if wr.committed() {
				panic(http.ErrAbortHandler) // A partial response cannot be replaced or completed safely.
			}
			http.Error(&wr, "internal server error", http.StatusInternalServerError)
		}()
		next.ServeHTTP(&wr, r)
	}
}

// responseRecorder wraps [http.ResponseWriter] to record status and bytes written.
type responseRecorder struct {
	http.ResponseWriter
	status     int // Zero means unwritten; 1xx is informational except HTTP/1.x 101.
	protoMajor int
	numBytes   int
}

// Write records bytes accepted by the writer and the implicit final 200 status.
func (re *responseRecorder) Write(b []byte) (int, error) {
	if !re.committed() {
		re.status = http.StatusOK // Leave implicit header handling, including content sniffing, to Write.
	}
	n, err := re.ResponseWriter.Write(b)
	re.numBytes += n
	return n, err
}

// WriteHeader remembers the last informational header or the first final header.
func (re *responseRecorder) WriteHeader(statusCode int) {
	if re.committed() {
		return
	}
	re.ResponseWriter.WriteHeader(statusCode)
	re.status = statusCode
}

// FlushError records net/http's implicit 200 even if flushing buffered data fails.
func (re *responseRecorder) FlushError() error {
	err := http.NewResponseController(re.ResponseWriter).Flush()
	if !errors.Is(err, http.ErrNotSupported) && !re.committed() {
		re.status = http.StatusOK
	}
	return err
}

// SetWriteDeadline supports pprof's deadline extension without exposing Unwrap,
// which would also allow connection takeover to bypass response accounting.
func (re *responseRecorder) SetWriteDeadline(deadline time.Time) error {
	return http.NewResponseController(re.ResponseWriter).SetWriteDeadline(deadline)
}

// committed reports whether a final status has been accepted. net/http treats
// 101 as final only for HTTP/1.x; every 1xx header is informational under HTTP/2.
func (re *responseRecorder) committed() bool {
	return re.status >= 200 || (re.protoMajor == 1 && re.status == http.StatusSwitchingProtocols)
}
