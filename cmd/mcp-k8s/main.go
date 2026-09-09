package main

import (
	"os"

	"github.com/agnaldom/mcp-k8s/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
