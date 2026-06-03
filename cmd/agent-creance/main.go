// Command agent-creance starts a coding agent inside an isolated, egress-filtered
// cage. See docs/design.md for the full design.
//
// This file is deliberately tiny: the Go convention is "main wires, packages
// do." All real logic lives under internal/, which keeps it testable and keeps
// this entrypoint to a single call.
package main

import (
	"os"

	"github.com/tobyS/agent-creance/internal/cli"
)

func main() {
	os.Exit(cli.Main())
}
