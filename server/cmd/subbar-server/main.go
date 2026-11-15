package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/letrahoo/sub-cost-view/server/internal/api"
	"github.com/letrahoo/sub-cost-view/server/internal/poller"
	"github.com/letrahoo/sub-cost-view/server/internal/providers"
	"github.com/letrahoo/sub-cost-view/server/internal/store"
)

func main() {
	defaultAddr := strings.TrimSpace(os.Getenv("SUBBAR_ADDR"))
	if defaultAddr == "" {
		defaultAddr = "127.0.0.1:8328"
	}
	defaultDir, _ := store.DefaultDataDir()

	addrFlag := flag.String("addr", defaultAddr, "HTTP listen address (default loopback 127.0.0.1:8328)")
	dataDirFlag := flag.String("data-dir", defaultDir, "Persistent owner-only data directory")
	allowLANFlag := flag.Bool("allow-non-loopback", false, "Explicitly permit binding non-loopback interfaces")
	flag.Parse()

	host, _, err := net.SplitHostPort(*addrFlag)
	if err != nil {
		log.Fatalf("invalid -addr %q: %v", *addrFlag, err)
	}
	if !*allowLANFlag {
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			log.Fatalf("refusing non-loopback bind %q without -allow-non-loopback (put a reverse proxy in front for remote access)", *addrFlag)
		}
	}

	st, err := store.Open(*dataDirFlag)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	if err := providers.LoadOAuthClients(filepath.Join(st.Dir(), "oauth-clients.json")); err != nil {
		log.Fatalf("load oauth-clients.json: %v", err)
	}

	reg := providers.NewRegistry()
	pl := poller.New(st, reg)
	srv := api.New(st, reg, pl)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go pl.Run(ctx)

	httpServer := &http.Server{
		Addr:              *addrFlag,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("subbar-server %s listening on http://%s (data: %s)", api.Version, *addrFlag, st.Dir())
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	srv.Shutdown()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutCtx)
}
