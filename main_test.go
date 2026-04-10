package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
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

// TestMain starts a real server for integration tests.
func TestMain(m *testing.M) {
	port := func() string { // Get a free port to run the server
		listener, err := net.Listen("tcp", ":0")
		if err != nil {
			log.Fatalf("failed to listen: %v", err)
		}
		defer listener.Close() //nolint:errcheck
		addr := listener.Addr().(*net.TCPAddr)
		return strconv.Itoa(addr.Port)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { // Start the server in a goroutine
		getenv := func(key string) string {
			if key == "PORT" {
				return port
			}
			return ""
		}
		if err := run(ctx, os.Stdout, getenv, "vtest"); err != nil {
			cancel()
			log.Fatal(err)
		}
	}()

	endpoint = "http://localhost:" + port

	start := time.Now() // wait for server to be healthy before tests.
	for time.Since(start) < 3*time.Second {
		if res, err := http.Get(endpoint + "/health"); err == nil && res.StatusCode == http.StatusOK {
			_ = res.Body.Close()
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	exitCode := m.Run()
	cancel()
	os.Exit(exitCode)
}

var endpoint string

// TestGetHealth tests the /health endpoint against the real server.
func TestGetHealth(t *testing.T) {
	t.Parallel()
	type response struct {
		Version        string    `json:"version"`
		Uptime         string    `json:"uptime"`
		LastCommitHash string    `json:"lastCommitHash"`
		LastCommitTime time.Time `json:"lastCommitTime"`
		DirtyBuild     bool      `json:"dirtyBuild"`
	}

	res, err := http.Get(endpoint + "/health")
	testNil(t, err)
	t.Cleanup(func() {
		err = res.Body.Close()
		testNil(t, err)
	})
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
			if tt.wantPanic {
				testContains(t, "panic!", buffer.String())
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
