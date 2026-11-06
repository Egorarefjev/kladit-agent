package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Egorarefjev/kladit-agent/internal/connection"
	"github.com/Egorarefjev/kladit-agent/internal/metrics"
	"github.com/Egorarefjev/kladit-agent/internal/policy"
)

const version = "0.3.0"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, out, stderr io.Writer) int {
	if len(args) == 0 {
		printHelp(out)
		return 0
	}
	command := args[0]
	switch command {
	case "help", "--help", "-h", "version", "--version":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "Unexpected arguments")
			return 2
		}
		if command == "version" || command == "--version" {
			fmt.Fprintln(out, "kladit-agent", version)
		} else {
			printHelp(out)
		}
		return 0
	case "enroll":
		flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
		flags.SetOutput(stderr)
		endpoint := flags.String("url", "", "HTTPS enrollment URL")
		directory := flags.String("directory", "", "Private staging directory")
		if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *endpoint == "" || *directory == "" {
			return 2
		}
		if err := connection.Enroll(ctx, *endpoint, os.Stdin, *directory, version); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "metrics", "policy-check", "send":
	default:
		fmt.Fprintln(stderr, "Unknown command. Use: kladit-agent help")
		return 2
	}

	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	policyPath := flags.String("policy", "/etc/kladit-agent/policy.json", "Local policy file")
	var configPath, tokenPath string
	if command == "send" {
		flags.StringVar(&configPath, "config", "/etc/kladit-agent/connection.json", "Connection config file")
		defaultToken := "/etc/kladit-agent/token"
		if directory := os.Getenv("CREDENTIALS_DIRECTORY"); directory != "" {
			defaultToken = filepath.Join(directory, "agent-token")
		}
		flags.StringVar(&tokenPath, "token-file", defaultToken, "Private machine token file")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "Unexpected arguments")
		return 2
	}

	if err := execute(ctx, command, *policyPath, configPath, tokenPath, out); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func execute(ctx context.Context, command, policyPath, configPath, tokenPath string, out io.Writer) error {
	p, err := policy.Load(policyPath)
	if err != nil {
		return err
	}
	if command == "policy-check" {
		_, err = fmt.Fprintln(out, "Policy is valid")
		return err
	}
	if err := p.Require("metrics.read"); err != nil {
		return err
	}

	var client *connection.Client
	if command == "send" {
		config, err := connection.LoadConfig(configPath)
		if err != nil {
			return err
		}
		token, err := connection.LoadToken(tokenPath)
		if err != nil {
			return err
		}
		client, err = connection.NewClient(config, token, version)
		if err != nil {
			return err
		}
	}

	snapshot, err := metrics.Collect(ctx)
	if err != nil {
		return err
	}
	if command == "send" {
		// Local consent may have been revoked while CPU counters were sampled.
		p, err = policy.Load(policyPath)
		if err != nil {
			return err
		}
		if err := p.Require("metrics.read"); err != nil {
			return err
		}
		return client.Send(ctx, snapshot)
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(snapshot)
}

func printHelp(out io.Writer) {
	fmt.Fprintln(out, `Kladit Agent

Usage:
  kladit-agent enroll --url HTTPS_URL --directory PATH < enrollment-token
  kladit-agent version
  kladit-agent metrics [--policy PATH]
  kladit-agent policy-check [--policy PATH]
  kladit-agent send [--policy PATH] [--config PATH] [--token-file PATH]`)
}
