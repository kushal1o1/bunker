package main

import (
	"fmt"
	"os"

	"github.com/kushal1o1/bunker/internal/cli"
)

func main() {
	if err := cli.Execute(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}