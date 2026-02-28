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
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
)

// NewCommand returns a new config edit command that opens the configuration file in an editor.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	return &appcmd.Command{
		Use:   name,
		Short: "Edit the configuration file in $EDITOR",
		Args:  appcmd.NoArgs,
		Run: builder.NewRunFunc(
			func(ctx context.Context, container appext.Container) error {
				return run(ctx, container)
			},
		),
	}
}

func run(ctx context.Context, container appext.Container) error {
	configDirPath := container.ConfigDirPath()
	configFilePath := ibctlconfig.ConfigFilePath(configDirPath)
	// Create the configuration file with the default template if it does not exist.
	if _, err := os.Stat(configFilePath); os.IsNotExist(err) {
		if _, err := ibctlconfig.InitConfig(configDirPath); err != nil {
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
