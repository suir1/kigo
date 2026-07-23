package main

import (
	"fmt"
	"os"

	"github.com/suir1/kigo/internal/app"
)

func main() {
	if err := app.NewRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", app.FormatError(err))
		os.Exit(1)
	}
}
