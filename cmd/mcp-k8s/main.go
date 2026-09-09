package main

import (
	"fmt"
	"os"
)

// This is the bootstrap placeholder from spec §13 step 01. The Cobra CLI
// (step 02) replaces this entrypoint with the real command tree:
// serve, doctor, config validate, cluster list, cluster test, version.
func main() {
	fmt.Fprintln(os.Stderr, "mcp-k8s: CLI not implemented yet (spec §13 step 02)")
	os.Exit(1)
}
