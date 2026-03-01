// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package configedit implements the "config edit" command.
package configedit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/ibctlcmd"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlpath"
	"github.com/spf13/pflag"
)

// NewCommand returns a new config edit command that opens the configuration file in an editor.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	flags := newFlags()
	return &appcmd.Command{
		Use:   name,
		Short: "Edit the configuration file in $EDITOR",
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
	// Dir is the ibctl directory containing ibctl.yaml.
	Dir string
}

func newFlags() *flags {
	return &flags{}
}

// Bind registers the flag definitions with the given flag set.
func (f *flags) Bind(flagSet *pflag.FlagSet) {
	flagSet.StringVar(&f.Dir, ibctlcmd.DirFlagName, ".", "The ibctl directory containing ibctl.yaml")
}

func run(ctx context.Context, container appext.Container, flags *flags) error {
	// Resolve the config file path from the base directory.
	configFilePath := ibctlpath.ConfigFilePath(flags.Dir)
	// Create the configuration file with the default template if it does not exist.
	if _, err := os.Stat(configFilePath); os.IsNotExist(err) {
		if err := ibctlconfig.InitConfig(flags.Dir); err != nil {
			return err
		}
	}
	// Determine the editor from the EDITOR environment variable.
	editor := container.Env("EDITOR")
	if editor == "" {
		return errors.New("EDITOR environment variable is not set")
	}
	// Open the configuration file in the editor.
	cmd := exec.CommandContext(ctx, editor, configFilePath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running editor: %w", err)
	}
	// Print the path of the edited file.
	_, err := fmt.Fprintf(container.Stdout(), "%s\n", configFilePath)
	return err
}
