package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/azizu06/rehearse/internal/controlplane"
	"github.com/azizu06/rehearse/internal/dashboard"
)

var version = "dev"

func main() {
	address := flag.String("addr", "127.0.0.1:8484", "control plane listen address")
	flag.Parse()

	dashboardHandler, err := dashboard.Handler()
	if err != nil {
		log.Fatalf("prepare dashboard: %v", err)
	}

	server := &http.Server{
		Addr:              *address,
		Handler:           controlplane.NewHandler(controlplane.Options{Version: version, Dashboard: dashboardHandler}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("Rehearse %s listening on http://%s", version, *address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve control plane: %v", err)
	}
}
