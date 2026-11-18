// Command iadb is adb for iPhone: control a real iPhone or iOS Simulator
// from the command line or from AI agents over MCP.
package main

import (
	"os"

	"github.com/iadb-dev/iadb/internal/cli"
)

var version = "dev"

func main() {
	cli.Version = version
	os.Exit(cli.Main(os.Args[1:]))
}
