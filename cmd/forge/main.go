package main

import (
	"os"

	"neuroforge/internal/cli"
)

func main() {
	os.Exit(cli.New().Run(os.Args[1:]))
}
