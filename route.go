package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"time"
)

func route() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/health", handleGetHealth())
	mux.Handle("/debug/", handleGetDebug())

	handler := accesslog(mux)
	handler = recovery(handler)
	return handler
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

func recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wm := responseMirror{ResponseWriter: w}
		defer func() {
			if err := recover(); err != nil {
				if err == http.ErrAbortHandler {
					// Handle the abort gracefully
					return
				}

				stack := make([]byte, 1024)
				n := runtime.Stack(stack, true)

				slog.ErrorContext(r.Context(), "panic!",
					slog.Any("error", err),
					slog.String("stack", string(stack[:n])),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("query", r.URL.RawQuery),
					slog.String("ip", r.RemoteAddr))

				if !wm.written() {
					http.Error(w, fmt.Sprintf("%v", err), 500)
				}
			}
		}()
		next.ServeHTTP(&wm, r)
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

func (re responseMirror) written() bool {
	return re.status != 0
}
