package main

import (
	"os"

	"github.com/poweur/cli/internal/cli"
	"github.com/poweur/cli/internal/config"
)

func main() {
	_ = config.LoadDotEnv(".env")
	code := cli.Run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}
