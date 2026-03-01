// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package download implements the "download" command.
package download

import (
	"context"

	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/ibctlcmd"
	"github.com/spf13/pflag"
)

// NewCommand returns a new download command that pre-caches IBKR data.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	flags := newFlags()
	return &appcmd.Command{
		Use:   name,
		Short: "Pre-cache IBKR data via Flex Query API",
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
	// Construct the downloader using shared command wiring.
	downloader, err := ibctlcmd.NewDownloader(container, flags.Dir)
	if err != nil {
		return err
	}
	// Download full history.
	return downloader.Download(ctx)
}
