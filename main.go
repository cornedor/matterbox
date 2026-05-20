package main

import (
	"os"

	"matterbox/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
