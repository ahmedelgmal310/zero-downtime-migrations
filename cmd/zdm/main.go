// Command zdm is the users API (one binary per release v1/v2/v3), its
// migration and backfill Jobs, and the e2e measurement tools.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/cli"
)

// version is the app release, set with -ldflags "-X main.version=v2".
var version = "v1"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], cli.Env{Version: version, Stdout: os.Stdout, Stderr: os.Stderr})
	stop()
	os.Exit(code)
}
