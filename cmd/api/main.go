package main

import (
	"log"
	"net/http"
	"os"

	"yunling.local/platform/internal/health"
)

func main() {
	address := os.Getenv("YUNLING_HTTP_ADDR")
	if address == "" {
		address = ":8080"
	}

	router := http.NewServeMux()
	router.Handle("GET /api/health", health.Handler())

	log.Printf("云令 API 正在监听 %s", address)
	if err := http.ListenAndServe(address, router); err != nil {
		log.Fatal(err)
	}
}
