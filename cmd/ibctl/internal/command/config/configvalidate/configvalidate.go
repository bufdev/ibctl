// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package configvalidate implements the "config validate" command.
package configvalidate

import (
	"context"

	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
)

// NewCommand returns a new config validate command that validates the configuration file.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	return &appcmd.Command{
		Use:   name,
		Short: "Validate the configuration file",
		Args:  appcmd.NoArgs,
		Run: builder.NewRunFunc(
			func(ctx context.Context, container appext.Container) error {
				return run(ctx, container)
			},
		),
	}
}

func run(_ context.Context, container appext.Container) error {
	return ibctlconfig.ValidateConfig(container.ConfigDirPath())
}
