package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/AmirIqbal1/shrinkray/internal/dashboard"
)

const version = "0.2.0"

type rootValues []string

func (values *rootValues) String() string { return strings.Join(*values, ", ") }
func (values *rootValues) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func main() {
	var roots rootValues
	var listen, shrinkrayBin, stateDir string
	var showVersion bool
	flag.Var(&roots, "root", "media directory or Display Name=/media/directory (required, repeatable)")
	flag.StringVar(&listen, "listen", "127.0.0.1:8787", "HTTP listen address")
	flag.StringVar(&shrinkrayBin, "shrinkray-bin", "shrinkray", "path to the shrinkray CLI")
	flag.StringVar(&stateDir, "state-dir", defaultStateDir(), "server state directory")
	flag.BoolVar(&showVersion, "version", false, "print the server version")
	flag.Parse()

	if showVersion {
		fmt.Printf("shrinkray-server v%s\n", version)
		return
	}
	if len(roots) == 0 {
		fmt.Fprintln(os.Stderr, "shrinkray-server: --root is required")
		flag.Usage()
		os.Exit(2)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		log.Fatalf("create state directory: %v", err)
	}

	registry, err := dashboard.NewRootRegistry(roots)
	if err != nil {
		log.Fatal(err)
	}
	srv, err := dashboard.NewServer(registry, shrinkrayBin, stateDir, version)
	if err != nil {
		log.Fatal(err)
	}
	defer srv.Close()

	if !isLoopbackListenAddress(listen) {
		log.Print("WARNING: Shrinkray has no authentication. Only expose it to a trusted LAN, Tailscale network or protected reverse proxy.")
	}

	httpServer := &http.Server{
		Addr:              listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatal(err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		log.Print("shutting down")
		srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("graceful shutdown: %v", err)
		}
	}()

	log.Printf("shrinkray-server v%s listening on http://%s", version, listener.Addr())
	if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func defaultStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".local", "share", "shrinkray", "server")
	}
	return filepath.Join(home, ".local", "share", "shrinkray", "server")
}

func isLoopbackListenAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
