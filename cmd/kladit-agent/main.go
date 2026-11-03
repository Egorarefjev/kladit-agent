package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/Egorarefjev/kladit-agent/internal/metrics"
	"github.com/Egorarefjev/kladit-agent/internal/policy"
	"os"
)

const version = "0.1.0-dev"

func main() {
	args := os.Args[1:]

	if len(args) == 0 {
		printHelp()
		return
	}

	if args[0] == "metrics" || args[0] == "policy-check" {
		runProtected(args)
		return
	}

	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Expected one command. Use: kladit-agent help")
		os.Exit(2)
	}

	switch args[0] {
	case "version", "--version":
		fmt.Println("kladit-agent", version)
	case "help", "--help", "-h":
		printHelp()
	default:
		fmt.Fprintln(os.Stderr, "Unknown command. Use: kladit-agent help")
		os.Exit(2)
	}
}

func printHelp() {
	fmt.Println(`Kladit Agent

Usage:
  kladit-agent help      Show this help
  kladit-agent version   Show the version
  kladit-agent metrics [--policy PATH]       Print Linux metrics as JSON
  kladit-agent policy-check [--policy PATH]  Validate local policy`)
}

func runProtected(args []string) {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	path := flags.String("policy", "/etc/kladit-agent/policy.json", "Local policy file")
	if err := flags.Parse(args[1:]); err != nil {
		if err != flag.ErrHelp {
			os.Exit(2)
		}
		return
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Unexpected arguments")
		os.Exit(2)
	}
	p, err := policy.Load(*path)
	if err == nil && args[0] == "metrics" {
		err = p.Require("metrics.read")
	}
	if err == nil {
		if args[0] == "policy-check" {
			fmt.Println("Policy is valid")
		} else {
			snapshot, collectErr := metrics.Collect()
			err = collectErr
			if err == nil {
				encoder := json.NewEncoder(os.Stdout)
				encoder.SetIndent("", "  ")
				err = encoder.Encode(snapshot)
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
