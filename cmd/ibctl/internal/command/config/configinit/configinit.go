// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package configinit implements the "config init" command.
package configinit

import (
	"context"
	"fmt"

	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
)

// NewCommand returns a new config init command that creates a default configuration file.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	return &appcmd.Command{
		Use:   name,
		Short: "Create a new configuration file",
		Args:  appcmd.NoArgs,
		Run: builder.NewRunFunc(
			func(ctx context.Context, container appext.Container) error {
				return run(ctx, container)
			},
		),
	}
}

func run(_ context.Context, container appext.Container) error {
	// Create the configuration file in the standard config directory.
	filePath, err := ibctlconfig.InitConfig(container.ConfigDirPath())
	if err != nil {
		return err
	}
	// Print the path of the created file so the user knows where to find it.
	_, err = fmt.Fprintf(container.Stdout(), "%s\n", filePath)
	return err
}
