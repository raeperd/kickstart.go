package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"time"
)

func accesslog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wr := responseRecorder{ResponseWriter: w}

		next.ServeHTTP(&wr, r)

		slog.InfoContext(r.Context(), "accessed",
			slog.String("latency", time.Since(start).String()),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("query", r.URL.RawQuery),
			slog.String("ip", r.RemoteAddr),
			slog.Int("status", wr.status),
			slog.Int("bytes", wr.numBytes))
	})
}

func recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wr := responseRecorder{ResponseWriter: w}
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

				if !wr.written() {
					http.Error(w, fmt.Sprintf("%v", err), 500)
				}
			}
		}()
		next.ServeHTTP(&wr, r)
	})
}

type responseRecorder struct {
	http.ResponseWriter
	status   int
	numBytes int
}

func (re *responseRecorder) Header() http.Header {
	return re.ResponseWriter.Header()
}

func (re *responseRecorder) Write(b []byte) (int, error) {
	re.numBytes += len(b)
	return re.ResponseWriter.Write(b)
}

func (re *responseRecorder) WriteHeader(statusCode int) {
	re.status = statusCode
	re.ResponseWriter.WriteHeader(statusCode)
}

func (re responseRecorder) written() bool {
	return re.status != 0
}
