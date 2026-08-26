package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// healthcheck — подкоманда для Docker HEALTHCHECK: проверяет /healthz без
// внешних утилит (системный curl ломается от LD_LIBRARY_PATH демона).
func healthcheck() int {
	addr := envOr("DETOUR_HTTP_ADDR", ":8080")
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.Status)
		return 1
	}
	return 0
}
