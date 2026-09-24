// Command water is the CEO digital-twin CLI.
package main

import (
	"os"

	"water/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:]))
}
