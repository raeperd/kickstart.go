package main

import (
	"log/slog"
	"net/http"
	"time"
)

func route() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/health", handleGetHealth())
	mux.Handle("/debug/", handleGetDebug())
	return accesslog(mux)
}

func accesslog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wm := responseMirror{ResponseWriter: w}

		next.ServeHTTP(&wm, r)

		slog.InfoContext(r.Context(), "accessed",
			slog.String("latency", time.Since(start).String()),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("query", r.URL.RawQuery),
			slog.String("ip", r.RemoteAddr),
			slog.Int("status", wm.status),
			slog.Int("bytes", wm.numBytes))
	})
}

type responseMirror struct {
	http.ResponseWriter
	status   int
	numBytes int
}

func (re *responseMirror) Header() http.Header {
	return re.ResponseWriter.Header()
}

func (re *responseMirror) Write(b []byte) (int, error) {
	re.numBytes += len(b)
	return re.ResponseWriter.Write(b)
}

func (re *responseMirror) WriteHeader(statusCode int) {
	re.status = statusCode
	re.ResponseWriter.WriteHeader(statusCode)
}
