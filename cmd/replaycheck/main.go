package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/justinzjj/TrustMap_prototype/Mapnode/replay"
)

func main() {
	runRoot := flag.String("run-root", "", "replay run root containing b0...b3 directories")
	golden := flag.String("golden", "", "expected replay aggregate JSON")
	artifactDir := flag.String("artifact-dir", "", "print semantic decision/path digests for one artifact directory")
	withPaths := flag.Bool("with-paths", false, "include paths.csv in --artifact-dir digest output")
	flag.Parse()
	if *artifactDir != "" {
		if *runRoot != "" || *golden != "" || flag.NArg() != 0 {
			fmt.Fprintln(os.Stderr, "--artifact-dir cannot be combined with --run-root/--golden or positional arguments")
			os.Exit(2)
		}
		digests, err := replay.ComputeReplaySemanticDigests(*artifactDir, *withPaths)
		if err != nil {
			fmt.Fprintf(os.Stderr, "replay digest failed: %v\n", err)
			os.Exit(1)
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(digests)
		return
	}
	if *runRoot == "" || *golden == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "--run-root and --golden are required")
		os.Exit(2)
	}
	if err := replay.CheckReplayGolden(*runRoot, *golden); err != nil {
		fmt.Fprintf(os.Stderr, "replay golden check failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("replay golden check: PASS run_root=%s golden=%s\n", *runRoot, *golden)
}
