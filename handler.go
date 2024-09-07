package main

import (
	"encoding/json"
	"net/http"
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
