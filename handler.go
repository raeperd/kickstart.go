package main

import (
	_ "embed"
	"encoding/json"
	"expvar"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"runtime/debug"
	"time"
)

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
