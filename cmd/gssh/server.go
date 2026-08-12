package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"gssh/internal/server"
)

// runDaemon runs the gssh daemon in the foreground. It is normally spawned
// detached by `gssh start` or by auto-start, but can be run directly for
// debugging (logs to stderr).
func runDaemon(args []string) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	sock := fs.String("S", socketPath, "Unix socket path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.SetOutput(os.Stderr)

	log.Printf("[daemon] Starting gssh server on %s", *sock)

	srv := server.NewServer(*sock)

	startErr := make(chan error, 1)
	go func() { startErr <- srv.Start() }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-startErr:
		if err != nil {
			// Fatal listen/startup error (Stop was never invoked).
			return err
		}
		// Start returned nil, which only happens after Stop began (via RPC
		// MsgStop). Wait for that Stop to finish: sync.Once blocks until the
		// in-flight call completes, so the process cannot exit mid-shutdown
		// (which would skip state persistence and socket cleanup).
		return srv.Stop()
	case <-sigCh:
		return srv.Stop()
	}
}
