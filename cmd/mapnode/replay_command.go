package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/justinzjj/TrustMap_prototype/Mapnode/replay"
)

func runReplay(arguments []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("mapnode replay", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to replay YAML configuration")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected positional arguments")
		return 2
	}
	if *configPath == "" {
		fmt.Fprintln(stderr, "--config is required")
		return 2
	}
	config, err := replay.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "mapnode replay config failed: %v\n", err)
		return 1
	}
	results, err := replay.RunReplay(context.Background(), config)
	if err != nil {
		fmt.Fprintf(stderr, "mapnode replay failed: %v\n", err)
		return 1
	}
	for _, result := range results {
		fmt.Fprintf(stderr, "mapnode replay setting=%s completed=%d run_dir=%s\n", result.Setting, result.CompletedEvents, result.RunDirectory)
	}
	return 0
}
