package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/marcusaroldan/load-balancer/internal/config"
	"github.com/marcusaroldan/load-balancer/internal/proxy"
)

func main() {
	cfg := config.Load()

	backends := []proxy.Backend{
		{ID: "backend-1", Address: "http://127.0.0.1:9001"},
		{ID: "backend-2", Address: "http://127.0.0.1:9002"},
	}

	handler := proxy.NewHandler(backends, proxy.FirstBackend)

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	log.Printf("starting load balancer on %s", addr)

	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatal(err)
	}
}
