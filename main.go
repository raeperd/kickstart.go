package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"io"
	"log/slog"
	"net"
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

	return requestOutcomes(mux, log)
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

// requestOutcomes owns response accounting, panic recovery, and the final access log.
func requestOutcomes(next http.Handler, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wr := responseRecorder{ResponseWriter: w, protoMajor: r.ProtoMajor}
		aborted := false
		defer func() {
			if !aborted && !wr.hijacked && !wr.committed() {
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
				slog.Bool("aborted", aborted),
				slog.Bool("hijacked", wr.hijacked))
		}()
		defer func() {
			err := recover()
			if err == nil {
				return
			}
			aborted = true
			if err == http.ErrAbortHandler {
				panic(http.ErrAbortHandler) // Let net/http close the connection or reset the HTTP/2 stream.
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
			if wr.committed() || wr.hijacked {
				panic(http.ErrAbortHandler) // A partial response cannot be replaced or completed safely.
			}
			http.Error(&wr, "internal server error", http.StatusInternalServerError)
			aborted = false
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
	hijacked   bool
}

// Write records bytes accepted by the writer and the implicit final 200 status.
func (re *responseRecorder) Write(b []byte) (int, error) {
	if !re.hijacked && !re.committed() {
		re.status = http.StatusOK // Leave implicit header handling, including content sniffing, to Write.
	}
	n, err := re.ResponseWriter.Write(b)
	re.numBytes += n
	return n, err
}

// WriteHeader remembers the last informational header or the first final header.
func (re *responseRecorder) WriteHeader(statusCode int) {
	if re.hijacked || re.committed() {
		return
	}
	re.ResponseWriter.WriteHeader(statusCode)
	re.status = statusCode
}

// Unwrap preserves ResponseController operations such as pprof's write deadline.
func (re *responseRecorder) Unwrap() http.ResponseWriter {
	return re.ResponseWriter
}

// Flush preserves the Flusher interface for streaming handlers.
func (re *responseRecorder) Flush() {
	_ = re.FlushError()
}

// FlushError intercepts flushes because a supported flush commits an implicit 200,
// even when flushing buffered data fails. An unsupported flush commits nothing.
func (re *responseRecorder) FlushError() error {
	w := re.ResponseWriter
	for {
		switch f := w.(type) {
		case interface{ FlushError() error }, http.Flusher:
			if !re.hijacked && !re.committed() {
				re.WriteHeader(http.StatusOK)
			}
			return http.NewResponseController(w).Flush()
		case interface{ Unwrap() http.ResponseWriter }:
			w = f.Unwrap()
		default:
			return http.ErrNotSupported
		}
	}
}

// Hijack transfers ownership of the connection; subsequent raw writes cannot be
// counted as HTTP response bytes, and net/http will not supply an implicit 200.
func (re *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(re.ResponseWriter).Hijack()
	if err == nil {
		re.hijacked = true
	}
	return conn, rw, err
}

// committed reports whether a final status has been accepted. net/http treats
// 101 as final only for HTTP/1.x; every 1xx header is informational under HTTP/2.
func (re *responseRecorder) committed() bool {
	return re.status >= 200 || (re.protoMajor == 1 && re.status == http.StatusSwitchingProtocols)
}
