package main

import (
	"fmt"
	"os"

	"github.com/justinzjj/TrustMap_prototype/internal/cli"
)

func main() {
	if err := cli.Execute(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "trustmapctl:", err)
		os.Exit(1)
	}
}
