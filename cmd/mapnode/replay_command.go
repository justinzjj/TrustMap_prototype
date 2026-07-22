package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/justinzjj/TrustMap_prototype/Mapnode/replay"
)

func runReplay(arguments []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("mapnode replay", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to replay YAML configuration")
	settingOverride := flags.String("setting", "", "run one setting (B0, B1, B2, B3) or all configured settings")
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
	if value := strings.TrimSpace(*settingOverride); value != "" && !strings.EqualFold(value, "all") {
		setting, parseErr := replay.ParseSetting(value)
		if parseErr != nil {
			fmt.Fprintf(stderr, "mapnode replay setting failed: %v\n", parseErr)
			return 2
		}
		configured := false
		for _, candidate := range config.Settings {
			if candidate == setting {
				configured = true
				break
			}
		}
		if !configured {
			fmt.Fprintf(stderr, "mapnode replay setting failed: %s is not present in the validated configuration\n", setting)
			return 2
		}
		config.Settings = []replay.Setting{setting}
	}
	results, err := replay.RunReplayWithProgress(context.Background(), config, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "mapnode replay failed: %v\n", err)
		return 1
	}
	for _, result := range results {
		fmt.Fprintf(stderr, "mapnode replay setting=%s completed=%d run_dir=%s\n", result.Setting, result.CompletedEvents, result.RunDirectory)
	}
	return 0
}
