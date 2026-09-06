package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"os"
	"slices"
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

// endpoint is set by TestMain; do not modify.
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

// TestAccessLogRecovery checks the response on the wire and its final access record.
func TestAccessLogRecovery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		handler       http.HandlerFunc
		status        int
		body          string
		panicLog      bool
		informational []int
		aborted       bool
		noResponse    bool
	}{
		{
			name: "normal response",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, "created")
			},
			status: http.StatusCreated,
			body:   "created",
		},
		{
			name:    "empty response",
			handler: func(http.ResponseWriter, *http.Request) {},
			status:  http.StatusOK,
		},
		{
			name:    "implicit status",
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") },
			status:  http.StatusOK,
			body:    "ok",
		},
		{
			name: "duplicate final headers",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				w.WriteHeader(http.StatusInternalServerError)
				w.WriteHeader(http.StatusEarlyHints)
			},
			status: http.StatusCreated,
		},
		{
			name: "informational then final",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusEarlyHints)
				w.WriteHeader(http.StatusProcessing)
				w.WriteHeader(http.StatusCreated)
			},
			status:        http.StatusCreated,
			informational: []int{http.StatusEarlyHints, http.StatusProcessing},
		},
		{
			name:          "informational then return",
			handler:       func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusEarlyHints) },
			status:        http.StatusOK,
			informational: []int{http.StatusEarlyHints},
		},
		{
			name: "informational then panic",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusEarlyHints)
				panic("private panic details")
			},
			status:        http.StatusInternalServerError,
			body:          "internal server error\n",
			panicLog:      true,
			informational: []int{http.StatusEarlyHints},
		},
		{
			name: "write deadline without connection takeover",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				controller := http.NewResponseController(w)
				if err := controller.SetWriteDeadline(time.Now().Add(time.Minute)); err != nil {
					panic(err)
				}
				conn, _, err := controller.Hijack()
				if conn != nil {
					_ = conn.Close()
				}
				if !errors.Is(err, http.ErrNotSupported) {
					panic("connection takeover should be unsupported")
				}
				w.WriteHeader(http.StatusCreated)
			},
			status: http.StatusCreated,
		},
		{
			name: "flush commits implicit status",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				if err := http.NewResponseController(w).Flush(); err != nil {
					panic(err)
				}
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, "ok")
			},
			status: http.StatusOK,
			body:   "ok",
		},
		{
			name: "panic after informational header and flush",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusEarlyHints)
				if err := http.NewResponseController(w).Flush(); err != nil {
					panic(err)
				}
				panic("private panic details")
			},
			status:        http.StatusOK,
			informational: []int{http.StatusEarlyHints},
			panicLog:      true,
			aborted:       true,
		},
		{
			name: "abort after flushed write",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, "partial")
				if err := http.NewResponseController(w).Flush(); err != nil {
					panic(err)
				}
				panic(http.ErrAbortHandler)
			},
			status:  http.StatusCreated,
			body:    "partial",
			aborted: true,
		},
		{
			name:       "abort before writing",
			handler:    func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) },
			aborted:    true,
			noResponse: true,
		},
		{
			name: "abort after informational header",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusEarlyHints)
				panic(http.ErrAbortHandler)
			},
			status:        http.StatusEarlyHints,
			informational: []int{http.StatusEarlyHints},
			aborted:       true,
			noResponse:    true,
		},
		{
			name: "panic after buffered write",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, "partial")
				panic("private panic details")
			},
			status:     http.StatusCreated,
			body:       "partial",
			panicLog:   true,
			aborted:    true,
			noResponse: true,
		},
		{
			name:     "panic before writing",
			handler:  func(http.ResponseWriter, *http.Request) { panic("private panic details") },
			status:   http.StatusInternalServerError,
			body:     "internal server error\n",
			panicLog: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buffer, nil))
			server := httptest.NewServer(accesslog(recovery(tt.handler, logger), logger))
			t.Cleanup(server.Close)
			server.Client().Timeout = 5 * time.Second
			var informational []int
			trace := &httptrace.ClientTrace{Got1xxResponse: func(code int, _ textproto.MIMEHeader) error {
				informational = append(informational, code)
				return nil
			}}
			req, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodGet, server.URL+"/test?key=value", nil)
			testNil(t, err)
			res, err := server.Client().Do(req)
			if tt.noResponse {
				if err == nil {
					_ = res.Body.Close()
					t.Fatal("expected the connection to abort without a final response")
				}
				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("expected connection EOF, got %v", err)
				}
			} else {
				testNil(t, err)
				body, readErr := io.ReadAll(res.Body)
				testNil(t, res.Body.Close())
				if tt.aborted {
					testEqual(t, io.ErrUnexpectedEOF, readErr)
				} else {
					testNil(t, readErr)
				}
				testEqual(t, tt.status, res.StatusCode)
				testEqual(t, tt.body, string(body))
			}
			if !slices.Equal(tt.informational, informational) {
				t.Fatalf("informational statuses: want %v; got %v", tt.informational, informational)
			}
			server.Close() // Wait for handlers before reading their logs.

			var accesses, panics int
			decoder := json.NewDecoder(&buffer)
			for decoder.More() {
				var record struct {
					Message string `json:"msg"`
					Method  string `json:"method"`
					Path    string `json:"path"`
					Query   string `json:"query"`
					IP      string `json:"ip"`
					Latency string `json:"latency"`
					Status  int    `json:"status"`
					Bytes   int    `json:"bytes"`
					Error   string `json:"error"`
					Stack   string `json:"stack"`
					Aborted bool   `json:"aborted"`
				}
				testNil(t, decoder.Decode(&record))
				switch record.Message {
				case "accessed":
					accesses++
					testEqual(t, http.MethodGet, record.Method)
					testEqual(t, "/test", record.Path)
					testEqual(t, "key=value", record.Query)
					testEqual(t, tt.status, record.Status)
					testEqual(t, tt.aborted, record.Aborted)
					testEqual(t, len(tt.body), record.Bytes)
					_, _, err := net.SplitHostPort(record.IP)
					testNil(t, err)
					_, err = time.ParseDuration(record.Latency)
					testNil(t, err)
				case "panic!":
					panics++
					testContains(t, "private panic details", record.Error)
					testContains(t, "goroutine", record.Stack)
				}
			}
			testEqual(t, 1, accesses)
			testEqual(t, tt.panicLog, panics == 1)
		})
	}
}

func TestAccessLogRecoveryInformationalProtocols(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		protoMajor int
		handler    http.HandlerFunc
		status     int
		body       string
	}{
		{
			name:       "HTTP2 explicit final status",
			protoMajor: 2,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, "created")
			},
			status: http.StatusCreated,
			body:   "created",
		},
		{
			name:       "HTTP2 implicit final status",
			protoMajor: 2,
			handler:    func(http.ResponseWriter, *http.Request) {},
			status:     http.StatusOK,
		},
		{
			name:       "HTTP2 recovered panic",
			protoMajor: 2,
			handler:    func(http.ResponseWriter, *http.Request) { panic("private panic details") },
			status:     http.StatusInternalServerError,
			body:       "internal server error\n",
		},
		{
			name:       "HTTP1 switching protocols is final",
			protoMajor: 1,
			handler:    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			status:     http.StatusSwitchingProtocols,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buffer, nil))
			handler := recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusSwitchingProtocols)
				tt.handler.ServeHTTP(w, r)
			}), logger)
			handler = accesslog(handler, logger)
			server := httptest.NewUnstartedServer(handler)
			server.EnableHTTP2 = tt.protoMajor == 2
			server.StartTLS()
			t.Cleanup(server.Close)
			server.Client().Timeout = 5 * time.Second
			var informational []int
			trace := &httptrace.ClientTrace{Got1xxResponse: func(code int, _ textproto.MIMEHeader) error {
				informational = append(informational, code)
				return nil
			}}
			req, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodGet, server.URL, nil)
			testNil(t, err)
			res, err := server.Client().Do(req)
			testNil(t, err)
			body, err := io.ReadAll(res.Body)
			testNil(t, res.Body.Close())
			testNil(t, err)
			testEqual(t, tt.protoMajor, res.ProtoMajor)
			testEqual(t, tt.status, res.StatusCode)
			testEqual(t, tt.body, string(body))
			if tt.protoMajor == 2 {
				testEqual(t, true, slices.Equal([]int{http.StatusSwitchingProtocols}, informational))
			} else {
				testEqual(t, 0, len(informational))
			}
			server.Close()
			decoder := json.NewDecoder(&buffer)
			accesses := 0
			for decoder.More() {
				var record struct {
					Message string `json:"msg"`
					Status  int    `json:"status"`
					Bytes   int    `json:"bytes"`
					Aborted bool   `json:"aborted"`
				}
				testNil(t, decoder.Decode(&record))
				if record.Message == "accessed" {
					accesses++
					testEqual(t, tt.status, record.Status)
					testEqual(t, len(tt.body), record.Bytes)
					testEqual(t, false, record.Aborted)
				}
			}
			testEqual(t, 1, accesses)
		})
	}
}

func TestAccessLogRecoveryImplicitHeaders(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, nil))
	handler := recovery(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html></html>")
	}), logger)
	handler = accesslog(handler, logger)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	res := recorder.Result()
	defer res.Body.Close() //nolint:errcheck
	testEqual(t, http.StatusOK, res.StatusCode)
	testEqual(t, "text/html; charset=utf-8", res.Header.Get("Content-Type"))
	var record struct {
		Status int `json:"status"`
		Bytes  int `json:"bytes"`
	}
	testNil(t, json.NewDecoder(&buffer).Decode(&record))
	testEqual(t, http.StatusOK, record.Status)
	testEqual(t, 13, record.Bytes)
}

func TestRootHTTPHandlerRoutes(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		path   string
		status int
		body   string
	}{
		{path: "/health", status: http.StatusOK, body: `"version":"vroot"`},
		{path: "/debug/vars", status: http.StatusOK, body: `"memstats"`},
		{path: "/debug/pprof/goroutine?debug=1", status: http.StatusOK, body: "goroutine profile"},
		{path: "/missing", status: http.StatusNotFound, body: "404 page not found"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			server := httptest.NewServer(newRootHTTPHandler(slog.New(slog.NewJSONHandler(&buffer, nil)), "vroot"))
			t.Cleanup(server.Close)
			server.Client().Timeout = 5 * time.Second
			res, err := server.Client().Get(server.URL + tt.path)
			testNil(t, err)
			body, err := io.ReadAll(res.Body)
			testNil(t, res.Body.Close())
			testNil(t, err)
			testEqual(t, tt.status, res.StatusCode)
			testContains(t, tt.body, string(body))
			server.Close()
			var record struct {
				Message string `json:"msg"`
				Status  int    `json:"status"`
				Bytes   int    `json:"bytes"`
			}
			decoder := json.NewDecoder(&buffer)
			testNil(t, decoder.Decode(&record))
			testEqual(t, "accessed", record.Message)
			testEqual(t, tt.status, record.Status)
			testEqual(t, len(body), record.Bytes)
			testEqual(t, false, decoder.More())
		})
	}
}

// Writer failures are exercised directly because a real connection cannot reliably
// force a particular short write or flush error.
func TestResponseRecorderWriteFailures(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		n    int
		err  error
	}{
		{name: "short write", n: 2},
		{name: "partial write with error", n: 2, err: io.ErrShortWrite},
		{name: "failed write", err: io.ErrClosedPipe},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			recorder := responseRecorder{ResponseWriter: writeFailure{ResponseWriter: httptest.NewRecorder(), n: tt.n, err: tt.err}}
			n, err := recorder.Write([]byte("hello"))
			testEqual(t, tt.n, n)
			testEqual(t, tt.err, err)
			testEqual(t, tt.n, recorder.numBytes)
			testEqual(t, http.StatusOK, recorder.status)
		})
	}
}

type writeFailure struct {
	http.ResponseWriter
	n   int
	err error
}

func (w writeFailure) Write([]byte) (int, error) { return w.n, w.err }

func TestResponseRecorderFlushFailures(t *testing.T) {
	t.Parallel()
	t.Run("unsupported flush does not commit", func(t *testing.T) {
		t.Parallel()
		underlying := httptest.NewRecorder()
		recorder := responseRecorder{ResponseWriter: struct{ http.ResponseWriter }{underlying}}
		err := http.NewResponseController(&recorder).Flush()
		testEqual(t, true, errors.Is(err, http.ErrNotSupported))
		recorder.WriteHeader(http.StatusCreated)
		testEqual(t, http.StatusCreated, underlying.Code)
		testEqual(t, http.StatusCreated, recorder.status)
	})
	t.Run("failed flush still commits", func(t *testing.T) {
		t.Parallel()
		underlying := httptest.NewRecorder()
		recorder := responseRecorder{ResponseWriter: flushFailure{underlying}}
		err := http.NewResponseController(&recorder).Flush()
		testEqual(t, io.ErrClosedPipe, err)
		recorder.WriteHeader(http.StatusInternalServerError)
		testEqual(t, http.StatusOK, underlying.Code)
		testEqual(t, http.StatusOK, recorder.status)
	})
}

type flushFailure struct{ http.ResponseWriter }

func (w flushFailure) FlushError() error {
	w.WriteHeader(http.StatusOK)
	return io.ErrClosedPipe
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
