package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/Egorarefjev/kladit-agent/internal/worker"
)

func runWorker(ctx context.Context, args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := worker.Options{Version: version, Log: stderr}
	flags.StringVar(&options.PolicyPath, "policy", "/etc/kladit-agent/worker-policy.json", "Separate worker policy")
	flags.StringVar(&options.ConfigPath, "config", "/etc/kladit-agent/connection.json", "Connection config")
	flags.StringVar(&options.TokenPath, "token-file", defaultTokenPath(), "Private machine token file")
	flags.StringVar(&options.StatePath, "state", "/var/lib/kladit-worker", "Private durable state directory")
	flags.StringVar(&options.DockerSocket, "docker-socket", "/run/docker.sock", "Local Docker socket")
	flags.BoolVar(&options.Check, "check", false, "Read-only local prerequisites check; no backend request")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "Unexpected arguments")
		return 2
	}
	if err := worker.Run(ctx, options); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
