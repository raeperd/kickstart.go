package main

import (
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
	"runtime/debug"
	"syscall"
	"time"
)

func main() {
	ctx := context.Background()
	if err := run(ctx, os.Stdout, os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, w io.Writer, args []string) error {
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
		Handler: route(),
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

func route() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /health", handleGetHealth())
	mux.Handle("GET /openapi.yaml", handleGetOpenapi())
	mux.Handle("/debug/", handleGetDebug())

	handler := accesslog(mux)
	handler = recovery(handler)
	return handler
}

var Version string

func handleGetHealth() http.HandlerFunc {
	type responseBody struct {
		Version  string    `json:"version"`
		Revision string    `json:"vcs.revision"`
		Time     time.Time `json:"vcs.time"`
		Modified bool      `json:"vcs.modified"`
	}

	var res responseBody
	res.Version = Version
	buildInfo, _ := debug.ReadBuildInfo()
	for _, kv := range buildInfo.Settings {
		if kv.Value == "" {
			continue
		}
		switch kv.Key {
		case "vcs.revision":
			res.Revision = kv.Value
		case "vcs.time":
			res.Time, _ = time.Parse(time.RFC3339, kv.Value)
		case "vcs.modified":
			res.Modified = kv.Value == "true"
		}
	}

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		if err := json.NewEncoder(w).Encode(res); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

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

func handleGetOpenapi() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(200)
		if _, err := w.Write(openapi); err != nil {
			slog.ErrorContext(r.Context(), "failed to write openapi", slog.Any("error", err))
		}
	}
}

//go:embed api/openapi.yaml
var openapi []byte
