// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package datazip implements the "data zip" command.
package datazip

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/ibctlcmd"
	"github.com/spf13/pflag"
)

// outputFlagName is the flag name for the output zip file path.
const outputFlagName = "output"

// NewCommand returns a new data zip command that archives the base directory.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	flags := newFlags()
	return &appcmd.Command{
		Use:   name,
		Short: "Archive the ibctl directory to a zip file",
		Args:  appcmd.NoArgs,
		Run: builder.NewRunFunc(
			func(ctx context.Context, container appext.Container) error {
				return run(ctx, container, flags)
			},
		),
		BindFlags: flags.Bind,
	}
}

type flags struct {
	// Dir is the base directory containing ibctl.yaml and data subdirectories.
	Dir string
	// Output is the path to the output zip file.
	Output string
}

func newFlags() *flags {
	return &flags{}
}

// Bind registers the flag definitions with the given flag set.
func (f *flags) Bind(flagSet *pflag.FlagSet) {
	flagSet.StringVar(&f.Dir, ibctlcmd.DirFlagName, ".", "The ibctl directory containing ibctl.yaml")
	flagSet.StringVarP(&f.Output, outputFlagName, "o", "", "Output zip file path (required)")
}

func run(_ context.Context, container appext.Container, flags *flags) error {
	if flags.Output == "" {
		return appcmd.NewInvalidArgumentError("--output (-o) is required")
	}
	// Ensure the output path ends in .zip.
	if !strings.HasSuffix(flags.Output, ".zip") {
		return appcmd.NewInvalidArgumentError("output file must have a .zip extension")
	}
	// Resolve the base directory to an absolute path.
	absDirPath, err := filepath.Abs(flags.Dir)
	if err != nil {
		return fmt.Errorf("resolving directory path: %w", err)
	}
	// Verify the base directory exists.
	info, err := os.Stat(absDirPath)
	if err != nil {
		return fmt.Errorf("base directory not found: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", absDirPath)
	}
	// Create the output zip file.
	outputFile, err := os.Create(flags.Output)
	if err != nil {
		return fmt.Errorf("creating output file: %w", err)
	}
	defer outputFile.Close()
	zipWriter := zip.NewWriter(outputFile)
	defer zipWriter.Close()
	// Walk the base directory and add all files to the zip archive.
	if err := filepath.Walk(absDirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip the output zip file itself if it's inside the base directory.
		absOutput, _ := filepath.Abs(flags.Output)
		if path == absOutput {
			return nil
		}
		// Compute the relative path for the zip entry.
		relPath, err := filepath.Rel(absDirPath, path)
		if err != nil {
			return err
		}
		// Skip the root directory entry.
		if relPath == "." {
			return nil
		}
		// For directories, add a trailing slash entry.
		if info.IsDir() {
			_, err := zipWriter.Create(relPath + "/")
			return err
		}
		// Add the file to the zip archive.
		writer, err := zipWriter.Create(relPath)
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(writer, file)
		return err
	}); err != nil {
		return fmt.Errorf("creating zip archive: %w", err)
	}
	// Close the zip writer to flush the archive.
	if err := zipWriter.Close(); err != nil {
		return fmt.Errorf("finalizing zip archive: %w", err)
	}
	logger := container.Logger()
	logger.Info("zip archive created", "path", flags.Output)
	return nil
}
