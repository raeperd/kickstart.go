package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"syscall"
)

func main() {
	var port uint
	flag.UintVar(&port, "port", 8080, "port for http api")
	flag.Parse()

	handler := http.NewServeMux()
	handler.HandleFunc("/ping", handleHealthCheck("pong"))
	handler.HandleFunc("/debug/pprof/", pprof.Index)
	handler.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	handler.HandleFunc("/debug/pprof/profile", pprof.Profile)
	handler.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	handler.HandleFunc("/debug/pprof/trace", pprof.Trace)
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: handler,
	}

	go func() {
		log.Printf("Starting http server for :%d", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	exit := make(chan os.Signal, 1)
	signal.Notify(exit, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	sig := <-exit
	if err := server.Shutdown(context.Background()); err != nil {
		log.Fatalf("server shutdown error: %v", err)
	}
	log.Printf("server shutdown with code: %v", sig)
}

func handleHealthCheck(message string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		response := message
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(message)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(response))
	}
}
