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
	"github.com/spf13/pflag"
)

// configFlagName is the flag name for the configuration file path.
const configFlagName = "config"

// NewCommand returns a new config validate command that validates a configuration file.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	flags := newFlags()
	return &appcmd.Command{
		Use:   name,
		Short: "Validate a configuration file",
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
	// Config is the path to the configuration file.
	Config string
}

func newFlags() *flags {
	return &flags{}
}

// Bind registers the flag definitions with the given flag set.
func (f *flags) Bind(flagSet *pflag.FlagSet) {
	flagSet.StringVar(
		&f.Config,
		configFlagName,
		"ibctl.yaml",
		"The configuration file path",
	)
}

func run(_ context.Context, _ appext.Container, flags *flags) error {
	if flags.Config == "" {
		return appcmd.NewInvalidArgumentErrorf("--%s is required", configFlagName)
	}
	return ibctlconfig.ValidateConfigFile(flags.Config)
}
