package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain provides one real server for integration tests and waits for its shutdown.
func TestMain(m *testing.M) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	endpoint = "http://127.0.0.1:" + port
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		getenv := func(key string) string {
			if key == "PORT" {
				return port
			}
			return ""
		}
		done <- run(ctx, os.Stdout, getenv, "vtest")
	}()

	client := &http.Client{Timeout: 250 * time.Millisecond}
	ready := false
	for start := time.Now(); time.Since(start) < 3*time.Second; {
		select {
		case err := <-done:
			fmt.Fprintln(os.Stderr, "server stopped before tests:", err)
			os.Exit(1)
		default:
		}
		res, err := client.Get(endpoint + "/health")
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	client.CloseIdleConnections()
	if !ready {
		fmt.Fprintln(os.Stderr, "server did not become healthy before tests")
		os.Exit(1)
	}

	exitCode := m.Run()
	cancel()
	if err := <-done; err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitCode = 1
	}
	os.Exit(exitCode)
}

// endpoint is set by TestMain; do not modify.
var endpoint string

// TestGetHealth tests the /health endpoint against the shared server.
func TestGetHealth(t *testing.T) {
	t.Parallel()
	type response struct {
		Version        string    `json:"version"`
		Uptime         string    `json:"uptime"`
		LastCommitHash string    `json:"lastCommitHash"`
		LastCommitTime time.Time `json:"lastCommitTime"`
		DirtyBuild     bool      `json:"dirtyBuild"`
	}

	client := &http.Client{Timeout: 3 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	res, err := client.Get(endpoint + "/health")
	testNil(t, err)
	t.Cleanup(func() { testNil(t, res.Body.Close()) })
	testEqual(t, http.StatusOK, res.StatusCode)
	testEqual(t, "application/json", res.Header.Get("Content-Type"))

	var body response
	testNil(t, json.NewDecoder(res.Body).Decode(&body))
	testEqual(t, "vtest", body.Version)
	if body.Uptime == "" {
		t.Fatal("expected non-empty Uptime")
	}
}

// TestRunPort tests invalid PORT values.
func TestRunPort(t *testing.T) {
	t.Parallel()

	invalidTests := []struct {
		name string
		port string
	}{
		{"not a number", "abc"},
		{"out of range", "70000"},
		{"zero", "0"},
		{"negative", "-1"},
	}
	for _, tt := range invalidTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			getenv := func(key string) string {
				if key == "PORT" {
					return tt.port
				}
				return ""
			}
			err := run(context.Background(), io.Discard, getenv, "vtest")
			if err == nil {
				t.Fatal("expected error for invalid PORT")
			}
			testContains(t, "invalid PORT", err.Error())
		})
	}
}

// TestRunBindError verifies a failed bind cannot report successful startup or change global logging.
func TestRunBindError(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	testNil(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	getenv := func(string) string { return port }
	var logs bytes.Buffer
	defaultLogger := slog.Default()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	err = run(ctx, &logs, getenv, "vtest")
	if err == nil {
		t.Fatal("expected bind error for occupied port")
	}
	if strings.Contains(logs.String(), "server started") {
		t.Fatalf("failed bind reported successful startup: %s", &logs)
	}
	testEqual(t, defaultLogger, slog.Default())
}

// TestRunCanceled verifies startup with a canceled context completes shutdown.
func TestRunCanceled(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	testNil(t, err)
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	testNil(t, listener.Close())

	ctx, cancel := context.WithCancelCause(t.Context())
	cause := errors.New("test shutdown")
	cancel(cause)
	var logs bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- run(ctx, &logs, func(string) string { return port }, "vtest") }()
	select {
	case err := <-done:
		testNil(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("run did not finish after cancellation")
	}
	testContains(t, "shutting down server", logs.String())
	testContains(t, cause.Error(), logs.String())

	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatal("listener still accepts connections after run returned")
	}
}

// TestAccessLogMiddleware verifies logged fields match request/response.
func TestAccessLogMiddleware(t *testing.T) {
	t.Parallel()

	type record struct {
		Method string `json:"method"`
		Path   string `json:"path"`
		Query  string `json:"query"`
		Status int    `json:"status"`
		body   []byte `json:"-"`
		Bytes  int    `json:"bytes"`
	}

	tests := []record{
		{
			Method: "GET",
			Path:   "/test",
			Query:  "?key=value",
			Status: http.StatusOK,
			body:   []byte(`{"hello":"world"}`),
		},
		{
			Method: "POST",
			Path:   "/api",
			Status: http.StatusCreated,
			body:   []byte(`{"id":1}`),
		},
		{
			Method: "DELETE",
			Path:   "/users/1",
			Status: http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		name := strings.Join([]string{tt.Method, tt.Path, tt.Query, strconv.Itoa(tt.Status)}, " ")
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var buffer strings.Builder
			handler := accesslog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.Status)
				w.Write(tt.body) //nolint:errcheck
			}), slog.New(slog.NewJSONHandler(&buffer, nil)))

			req := httptest.NewRequest(tt.Method, tt.Path+tt.Query, bytes.NewReader(tt.body))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			var log record
			err := json.NewDecoder(strings.NewReader(buffer.String())).Decode(&log)
			testNil(t, err)

			testEqual(t, tt.Method, log.Method)
			testEqual(t, tt.Path, log.Path)
			testEqual(t, strings.TrimPrefix(tt.Query, "?"), log.Query)
			testEqual(t, len(tt.body), log.Bytes)
			testEqual(t, tt.Status, log.Status)
		})
	}
}

// TestRecoveryMiddleware verifies panic handling and ErrAbortHandler.
func TestRecoveryMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		hf        func(w http.ResponseWriter, r *http.Request)
		wantCode  int
		wantBody  string
		wantPanic bool
	}{
		{
			name: "no panic on http.ErrAbortHandler",
			hf: func(_ http.ResponseWriter, _ *http.Request) {
				panic(http.ErrAbortHandler)
			},
			wantCode:  http.StatusOK,
			wantPanic: false,
		},
		{
			name: "panic on http.Handler",
			hf: func(_ http.ResponseWriter, _ *http.Request) {
				panic("something went wrong")
			},
			wantCode:  http.StatusInternalServerError,
			wantBody:  "internal server error\n",
			wantPanic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buffer strings.Builder
			handler := recovery(http.HandlerFunc(tt.hf), slog.New(slog.NewTextHandler(&buffer, nil)))

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			testEqual(t, tt.wantCode, rec.Code)
			testEqual(t, tt.wantBody, rec.Body.String())
			if tt.wantPanic {
				testContains(t, "panic!", buffer.String())
				testContains(t, "something went wrong", buffer.String())
			}
		})
	}
}

func testEqual[T comparable](tb testing.TB, want, got T) {
	tb.Helper()
	if want != got {
		tb.Fatalf("want: %v; got: %v", want, got)
	}
}

func testNil(tb testing.TB, err error) {
	tb.Helper()
	testEqual(tb, nil, err)
}

func testContains(tb testing.TB, needle string, haystack string) {
	tb.Helper()
	if !strings.Contains(haystack, needle) {
		tb.Fatalf("%q not in %q", needle, haystack)
	}
}
