// Command zamnius is the process entry point. It does nothing but bind the
// process's arguments, streams, and signals to the command layer, then exit with
// the code that layer returns; every decision belongs to internal/cli and below
// (contracts/cli.md §1).
package main

import (
	"context"
	"os"

	"github.com/prompt-fu/zamnius/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
