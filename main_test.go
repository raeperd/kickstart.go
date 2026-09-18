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
	"runtime/trace"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain provides one real server for integration tests and waits for its shutdown.
func TestMain(m *testing.M) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	endpoint = "http://" + listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, os.Stdout, func(string) string { return "" }, "vtest",
			func(string, string) (net.Listener, error) { return listener, nil })
	}()

	// The listener is already bound; requests can connect while Serve starts.
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
			err := run(context.Background(), io.Discard, getenv, "vtest", net.Listen)
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
	err = run(ctx, &logs, getenv, "vtest", net.Listen)
	if err == nil {
		t.Fatal("expected bind error for occupied port")
	}
	if strings.Contains(logs.String(), "server started") {
		t.Fatalf("failed bind reported successful startup: %s", &logs)
	}
	testEqual(t, defaultLogger, slog.Default())
}

// TestRunShutdown exercises draining and timeout through the real profiling handler.
func TestRunShutdown(t *testing.T) {
	if trace.IsEnabled() {
		t.Skip("profiling handler requires exclusive tracing; unavailable with go test -trace")
	}
	// Tracing is process-wide, so these cases must run sequentially.
	for _, tt := range []struct {
		name        string
		seconds     string
		wantTimeout bool
	}{
		{name: "drains_active_request", seconds: "1"},
		{name: "closes_after_timeout", seconds: "30", wantTimeout: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() {
				deadline := time.After(5 * time.Second)
				for trace.IsEnabled() {
					select {
					case <-deadline:
						t.Fatal("profiling handler did not stop tracing after shutdown")
					case <-time.After(time.Millisecond):
					}
				}
			})
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			testNil(t, err)
			t.Cleanup(func() { _ = listener.Close() })
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				done <- run(ctx, io.Discard, func(string) string { return "" }, "vtest",
					func(string, string) (net.Listener, error) { return listener, nil })
				close(done)
			}()
			requestCtx, cancelRequest := context.WithCancel(t.Context())
			t.Cleanup(func() {
				cancelRequest()
				cancel()
				_ = testReceive(t, done)
			})

			client := &http.Client{Timeout: 20 * time.Second}
			t.Cleanup(client.CloseIdleConnections)
			req, err := http.NewRequestWithContext(requestCtx, http.MethodGet,
				"http://"+listener.Addr().String()+"/debug/pprof/trace?seconds="+tt.seconds, nil)
			testNil(t, err)
			response := make(chan error, 1)
			go func() {
				defer close(response)
				res, err := client.Do(req)
				if err != nil {
					response <- err
					return
				}
				defer res.Body.Close() //nolint:errcheck
				_, err = io.Copy(io.Discard, res.Body)
				if err == nil && res.StatusCode != http.StatusOK {
					err = fmt.Errorf("unexpected status: %d", res.StatusCode)
				}
				response <- err
			}()
			t.Cleanup(func() {
				cancelRequest()
				_ = testReceive(t, response)
			})

			// An enabled trace confirms the request reached the handler before cancellation.
			deadline := time.After(3 * time.Second)
			for !trace.IsEnabled() {
				select {
				case err := <-response:
					t.Fatalf("profiling request ended before shutdown: %v", err)
				case <-deadline:
					t.Fatal("profiling request did not start")
				case <-time.After(time.Millisecond):
				}
			}
			cancel()
			select {
			case err = <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("run did not finish shutdown")
			}
			if tt.wantTimeout {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expected shutdown deadline, got %v", err)
				}
				testContains(t, "server shutdown:", err.Error())
				if err := testReceive(t, response); err == nil || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expected connection closure before client deadline, got %v", err)
				}
			} else {
				testNil(t, err)
				testNil(t, testReceive(t, response))
			}
			conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
			if err == nil {
				_ = conn.Close()
				t.Fatal("listener still accepts connections after run returned")
			}
		})
	}
}

// TestRunCompletion covers cancellation before serving and an unexpected accept error.
func TestRunCompletion(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name              string
		cancelBeforeServe bool
	}{
		{name: "canceled_before_serving", cancelBeforeServe: true},
		{name: "closed_listener"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			testNil(t, err)
			t.Cleanup(func() { _ = listener.Close() })
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			cause := errors.New("test shutdown")
			if tt.cancelBeforeServe {
				cancel(cause)
			} else {
				testNil(t, listener.Close())
			}
			var logs bytes.Buffer
			done := make(chan error, 1)
			go func() {
				done <- run(ctx, &logs, func(string) string { return "" }, "vtest",
					func(string, string) (net.Listener, error) { return listener, nil })
				close(done)
			}()
			t.Cleanup(func() {
				cancel(nil)
				_ = testReceive(t, done)
			})
			err = testReceive(t, done)
			if tt.cancelBeforeServe {
				testNil(t, err)
				testContains(t, "shutting down server", logs.String())
				testContains(t, cause.Error(), logs.String())
				conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
				if err == nil {
					_ = conn.Close()
					t.Fatal("cancellation before serving left the listener open")
				}
			} else if !errors.Is(err, net.ErrClosed) {
				t.Fatalf("expected closed listener error, got %v", err)
			}
		})
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

// testReceive bounds waits for lifecycle events and goroutine completion.
func testReceive[T any](tb testing.TB, ch <-chan T) T {
	tb.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		tb.Fatal("timed out waiting for server lifecycle event")
		var zero T
		return zero
	}
}
