package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/saladtechnologies/salad-cloud-cli/internal/cli"
)

func main() {
	// One signal handler for the whole run: Ctrl-C cancels the root context.
	// A wait sees that and fails with kind interrupted; a request in flight
	// ends as "<command> canceled", also interrupted; an open-ended command
	// (logs --follow, --watch) stops cleanly with exit 0. After the first
	// signal the default handler is back, so a second Ctrl-C kills the process.
	root := context.Background() //nolint:forbidigo // main creates the root context
	ctx, stop := signal.NotifyContext(root, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	code := cli.Run(ctx, cli.ProcessDeps())
	stop()
	os.Exit(code) //nolint:forbidigo // only main exits
}
