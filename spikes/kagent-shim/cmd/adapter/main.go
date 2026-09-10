package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/kaimahi-agents/kaimahi/spikes/kagent-shim/internal/adapter"
)

func main() {
	configPath := "/config/routes.json"
	if len(os.Args) > 2 {
		log.Fatal("usage: adapter [routes.json]")
	}
	if len(os.Args) == 2 {
		configPath = os.Args[1]
	}
	handler, err := adapter.New(configPath, "/secrets")
	if err != nil {
		log.Fatal("adapter configuration failed")
	}
	server := &http.Server{
		Addr: ":8080", Handler: handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       45 * time.Second,
		WriteTimeout:      50 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	log.Print("adapter listening on :8080")
	if err := server.ListenAndServe(); err != nil {
		log.Fatal("adapter listener stopped")
	}
}
