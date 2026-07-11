package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

// runHealthcheck backs the container HEALTHCHECK.
//
// It exists because the final image is distroless: no shell, no curl, nothing to
// exec. The binary probes itself instead.
//
// It reads BACKEND_PORT directly rather than going through config.Load, because
// a health probe must not fail for the same reason the server would — its job is
// to report on the running server, not to re-validate the environment.
func runHealthcheck() int {
	port := os.Getenv("BACKEND_PORT")
	if port == "" {
		port = "8080"
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/readyz")
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
