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
	"github.com/bufdev/ibctl/cmd/ibctl/internal/ibctlcmd"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/spf13/pflag"
)

// NewCommand returns a new config init command that creates a default configuration file.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	flags := newFlags()
	return &appcmd.Command{
		Use:   name,
		Short: "Create a new configuration file",
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
	flagSet.StringVar(&f.Config, ibctlcmd.ConfigFlagName, ibctlconfig.DefaultConfigFileName, "The configuration file path")
}

func run(_ context.Context, container appext.Container, flags *flags) error {
	// Create the configuration file at the specified path.
	if err := ibctlconfig.InitConfig(flags.Config); err != nil {
		return err
	}
	// Print the path of the created file so the user knows where to find it.
	_, err := fmt.Fprintf(container.Stdout(), "%s\n", flags.Config)
	return err
}
