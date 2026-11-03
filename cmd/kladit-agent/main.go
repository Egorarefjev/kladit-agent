package main

import (
	"encoding/json"
	"fmt"
	"github.com/Egorarefjev/kladit-agent/internal/metrics"
	"os"
)

const version = "0.1.0-dev"

func main() {
	args := os.Args[1:]

	if len(args) == 0 {
		printHelp()
		return
	}

	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Expected one command. Use: kladit-agent help")
		os.Exit(2)
	}

	switch args[0] {
	case "metrics":
		snapshot, err := metrics.Collect()
		if err == nil {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			err = encoder.Encode(snapshot)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "Metrics:", err)
			os.Exit(1)
		}
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
  kladit-agent metrics   Print a Linux host metrics snapshot as JSON`)
}
