package main

import (
	"errors"
	"fmt"
	"os"

	"habit/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "habit:", err)
		code := 1
		var ec interface{ ExitCode() int }
		if errors.As(err, &ec) {
			code = ec.ExitCode()
		}
		os.Exit(code)
	}
}
