// Command iphoneuse is computer use for your iPhone: it lets AI agents (over
// MCP) and you (from the terminal) control a real iPhone or iOS Simulator.
package main

import (
	"os"

	"github.com/safwanadnan/iphoneuse/internal/cli"
)

var version = "dev"

func main() {
	cli.Version = version
	os.Exit(cli.Main(os.Args[1:]))
}
