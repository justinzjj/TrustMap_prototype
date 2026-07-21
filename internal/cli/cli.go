package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/justinzjj/TrustMap_prototype/internal/config"
	"github.com/justinzjj/TrustMap_prototype/internal/topology"
	"github.com/spf13/cobra"
)

func Execute(args []string, stdout, stderr io.Writer) error {
	command := NewRootCommand(stdout, stderr)
	command.SetArgs(args)
	return command.Execute()
}

func NewRootCommand(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "trustmapctl",
		Short:         "TrustMap local topology management",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.AddCommand(newTopologyCommand())
	return root
}

func newTopologyCommand() *cobra.Command {
	command := &cobra.Command{Use: "topology", Short: "Validate and render TrustMap topologies"}
	command.AddCommand(newValidateCommand(), newRenderCommand())
	return command
}

func newValidateCommand() *cobra.Command {
	var file string
	command := &cobra.Command{
		Use:   "validate",
		Short: "Validate a topology YAML file",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			parsed, err := config.LoadFile(file)
			if err != nil {
				return fmt.Errorf("validate topology: %w", err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "valid topology: %s (%d chains)\n", parsed.NetworkName, len(parsed.Chains))
			return err
		},
	}
	command.Flags().StringVar(&file, "file", "", "topology YAML file")
	_ = command.MarkFlagRequired("file")
	return command
}

func newRenderCommand() *cobra.Command {
	var file string
	var output string
	command := &cobra.Command{
		Use:   "render",
		Short: "Render a topology into a local runtime directory",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			runtimeOutput, err := runtimeOutputPath(output)
			if err != nil {
				return err
			}
			parsed, err := config.LoadFile(file)
			if err != nil {
				return fmt.Errorf("render topology: %w", err)
			}
			if err := topology.Render(parsed, runtimeOutput); err != nil {
				return fmt.Errorf("render topology to %q: %w", runtimeOutput, err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "rendered topology: %s (%d chains) to %s\n", parsed.NetworkName, len(parsed.Chains), runtimeOutput)
			return err
		},
	}
	command.Flags().StringVar(&file, "file", "", "topology YAML file")
	command.Flags().StringVar(&output, "output", "", "output directory")
	_ = command.MarkFlagRequired("file")
	_ = command.MarkFlagRequired("output")
	return command
}

func runtimeOutputPath(output string) (string, error) {
	repositoryRoot, err := findRepositoryRoot()
	if err != nil {
		return "", err
	}
	absoluteOutput, err := filepath.Abs(output)
	if err != nil {
		return "", fmt.Errorf("resolve output path: %w", err)
	}
	runtimeRoot := filepath.Join(repositoryRoot, "runtime")
	if filepath.Dir(filepath.Clean(absoluteOutput)) != runtimeRoot {
		return "", fmt.Errorf("output must be a direct child of the repository runtime directory %q", runtimeRoot)
	}
	if info, statErr := os.Lstat(absoluteOutput); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("output must not be a symbolic link: %q", absoluteOutput)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return "", fmt.Errorf("inspect output path %q: %w", absoluteOutput, statErr)
	}
	return absoluteOutput, nil
}

func findRepositoryRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	for {
		if info, statErr := os.Stat(filepath.Join(directory, "go.mod")); statErr == nil && info.Mode().IsRegular() {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("trustmapctl must be run from within the TrustMap_prototype repository")
		}
		directory = parent
	}
}
