package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
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

// TestMain starts the server and runs all the tests.
// By doing this, you can run **actual** integration tests without starting the server.
func TestMain(m *testing.M) {
	flag.Parse() // NOTE: this is needed to parse args from go test command

	port := func() string { // Get a free port to run the server
		listener, err := net.Listen("tcp", ":0")
		if err != nil {
			log.Fatalf("failed to listen: %v", err)
		}
		defer listener.Close()
		addr := listener.Addr().(*net.TCPAddr)
		return strconv.Itoa(addr.Port)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { // Start the server in a goroutine
		if err := run(ctx, os.Stdout, []string{"test", "--port", port}, "vtest"); err != nil {
			log.Fatal(err)
		}
	}()

	endpoint = "http://localhost:" + port

	start := time.Now() // wait for server to be healthy before tests.
	for time.Since(start) < 3*time.Second {
		if res, err := http.Get(endpoint + "/health"); err == nil && res.StatusCode == http.StatusOK {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	os.Exit(m.Run())
}

// endpoint holds the server endpoint started by TestMain, not intended to be updated.
var endpoint string

// TestGetHealth tests the /health endpoint.
// Server is started by [TestMain] so that the test can make requests to it.
func TestGetHealth(t *testing.T) {
	t.Parallel()
	// response is repeated but this describes intention of test better.
	// For example you can add fiels only needed for testing.
	type response struct {
		Version  string    `json:"version"`
		Revision string    `json:"vcs.revision"`
		Time     time.Time `json:"vcs.time"`
		// Modified bool      `json:"vcs.modified"`
	}

	// actual http request to the server.
	res, err := http.Get(endpoint + "/health")
	testNil(t, err)
	t.Cleanup(func() {
		err = res.Body.Close()
		testNil(t, err)
	})
	testEqual(t, http.StatusOK, res.StatusCode)
	testEqual(t, "application/json", res.Header.Get("Content-Type"))
	testNil(t, json.NewDecoder(res.Body).Decode(&response{}))
}

// TestGetOpenAPI tests the /openapi.yaml endpoint.
// You can add more test as needed without starting the server again.
func TestGetOpenAPI(t *testing.T) {
	t.Parallel()
	res, err := http.Get(endpoint + "/openapi.yaml")
	testNil(t, err)
	testEqual(t, http.StatusOK, res.StatusCode)
	testEqual(t, "text/plain", res.Header.Get("Content-Type"))

	sb := strings.Builder{}
	_, err = io.Copy(&sb, res.Body)
	testNil(t, err)
	t.Cleanup(func() {
		err = res.Body.Close()
		testNil(t, err)
	})

	testContains(t, "openapi: 3.1.0", sb.String())
	testContains(t, "version: ", sb.String())
}

// TestAccessLogMiddleware tests accesslog middleware
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

// TestRecoveryMiddleware tests recovery middleware
func TestRecoveryMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		hf        func(w http.ResponseWriter, r *http.Request)
		wantCode  int
		wantPanic bool
	}{
		{
			name: "no panic on normal http.Handler",
			hf: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("success")) //nolint:errcheck
			},
			wantCode:  http.StatusOK,
			wantPanic: false,
		},
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
