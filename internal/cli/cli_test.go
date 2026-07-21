package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justinzjj/TrustMap_prototype/internal/cli"
)

func TestTopologyValidateReportsSuccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := cli.Execute([]string{"topology", "validate", "--file", filepath.Join("..", "..", "configs", "topology.yaml")}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Execute() error = %v, stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "valid") || !strings.Contains(stdout.String(), "3 chains") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestTopologyValidateReturnsClearStrictDecodeError(t *testing.T) {
	input := filepath.Join(t.TempDir(), "invalid.yaml")
	content := strings.Replace(renderYAMLForCLI, "network_name: cli-test", "network_name: cli-test\nunknown: true", 1)
	if err := os.WriteFile(input, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := cli.Execute([]string{"topology", "validate", "--file", input}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unknown") || !strings.Contains(err.Error(), input) {
		t.Fatalf("expected contextual strict decode error, got %v", err)
	}
}

func TestTopologyRenderWritesOutput(t *testing.T) {
	input := filepath.Join(t.TempDir(), "topology.yaml")
	if err := os.WriteFile(input, []byte(renderYAMLForCLI), 0600); err != nil {
		t.Fatal(err)
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.MkdirTemp(filepath.Join(repositoryRoot, "runtime"), ".cli-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(output) })
	var stdout, stderr bytes.Buffer
	err = cli.Execute([]string{"topology", "render", "--file", input, "--output", output}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Execute() error = %v, stderr=%s", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(output, "compose.yaml")); err != nil {
		t.Fatalf("compose not rendered: %v", err)
	}
	if !strings.Contains(stdout.String(), output) || !strings.Contains(stdout.String(), "2 chains") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestTopologyRenderRejectsOutputOutsideRuntimeBoundary(t *testing.T) {
	input := filepath.Join(t.TempDir(), "topology.yaml")
	if err := os.WriteFile(input, []byte(renderYAMLForCLI), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := cli.Execute(
		[]string{"topology", "render", "--file", input, "--output", filepath.Join(t.TempDir(), "generated")},
		&stdout,
		&stderr,
	)
	if err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Fatalf("expected runtime-boundary error, got %v", err)
	}
}

func TestTopologyCommandsRequireFlags(t *testing.T) {
	tests := [][]string{{"topology", "validate"}, {"topology", "render", "--file", "missing.yaml"}}
	for _, args := range tests {
		var stdout, stderr bytes.Buffer
		if err := cli.Execute(args, &stdout, &stderr); err == nil {
			t.Fatalf("Execute(%v) unexpectedly succeeded", args)
		}
	}
}

const renderYAMLForCLI = `version: 1
network_name: cli-test
chains:
  - {name: alpha, chain_id: 9001}
  - {name: beta, chain_id: 9002}
mapnodes: {database: sqlite}
`
