package main

import (
	"fmt"
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
	fmt.Println(`Kladit Agent — development scaffold

Usage:
  kladit-agent help      Show this help
  kladit-agent version   Show the version

Server monitoring and backend connection are not implemented yet.`)
}
