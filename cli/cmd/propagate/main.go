package main

import (
	"os"

	"propagate/cli/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.Streams{
		In:  os.Stdin,
		Out: os.Stdout,
		Err: os.Stderr,
	}))
}
