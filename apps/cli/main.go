package main

import (
	"os"

	"github.com/eurything/cli/internal/cli"
	"github.com/eurything/cli/internal/config"
)

func main() {
	_ = config.LoadDotEnv(".env")
	code := cli.Run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}
