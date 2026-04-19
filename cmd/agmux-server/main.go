package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"agmux/internal/server"
	"agmux/internal/socketpath"
)

var version = "dev"

func main() {
	socketPath := flag.String("S", defaultSocketPath(), "Unix socket path")
	showVersion := flag.Bool("v", false, "Show version")
	flag.Parse()

	if *showVersion {
		println("agmux-server version " + version)
		os.Exit(0)
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.SetOutput(os.Stderr)

	log.Printf("[daemon] Starting agmux-server on %s", *socketPath)

	srv := server.NewServer(*socketPath)

	go func() {
		if err := srv.Start(); err != nil {
			log.Printf("[daemon] Server error: %v", err)
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	srv.Stop()
}

func defaultSocketPath() string {
	return socketpath.Default()
}
