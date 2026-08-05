// Command podkop-server is the VPS-side management panel for podkop routers.
// It issues keys (optionally via a companion 3x-UI panel), distributes route
// profiles to routers over /api/v1/profile, and fronts everything with a decoy
// site that camouflages the operator panel from active probing.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nifigaprikolno/podkop/server/internal/config"
	"github.com/nifigaprikolno/podkop/server/internal/httpapi"
	"github.com/nifigaprikolno/podkop/server/internal/store"
	"github.com/nifigaprikolno/podkop/server/internal/xui"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	st, err := store.Open(cfg.StorePath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	if _, err := st.EnsureDefaultProfile(); err != nil {
		log.Fatalf("seed default profile: %v", err)
	}

	var xc *xui.Client
	if cfg.XUIEnabled() {
		xc, err = xui.New(cfg.XUIBaseURL, cfg.XUIUsername, cfg.XUIPassword, cfg.XUIPublicHost)
		if err != nil {
			log.Fatalf("xui: %v", err)
		}
		log.Printf("3x-UI integration enabled (%s, inbound %d)", cfg.XUIBaseURL, cfg.XUIInbound)
	} else {
		log.Printf("3x-UI integration disabled (manual key mode)")
	}

	srv, err := httpapi.NewServer(cfg, st, xc)
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("podkop-server listening on %s (admin path %s)", cfg.Listen, cfg.AdminPath)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	log.Printf("podkop-server stopped")
}
