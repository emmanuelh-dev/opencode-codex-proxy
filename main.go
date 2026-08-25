package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	if err := loadDotEnv(".env"); err != nil && !os.IsNotExist(err) {
		log.Fatal(err)
	}

	proxy, err := newProxy(os.Getenv("OPENCODE_GO_API_KEY"), "https://opencode.ai/zen/go/v1")
	if err != nil {
		log.Fatal(err)
	}

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	log.Printf("OpenCode Codex proxy listening on http://%s", addr)
	if err := http.ListenAndServe(addr, proxy.handler()); err != nil {
		log.Fatal(fmt.Errorf("serve: %w", err))
	}
}
