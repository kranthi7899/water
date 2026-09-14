// Command water is the multi-agent persona CLI.
package main

import (
	"io/fs"
	"os"

	"water"
	"water/internal/cli"
)

func main() {
	agents, err := fs.Sub(water.AgentsFS(), "agents")
	if err != nil {
		panic(err)
	}
	os.Exit(cli.Execute(agents, os.Args[1:]))
}
