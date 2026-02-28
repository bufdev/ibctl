// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package download implements the "download" command.
package download

import (
	"context"
	"log/slog"

	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/bufdev/ibctl/internal/ibctl/ibctldownload"
	"github.com/spf13/pflag"
)

// configFlagName is the flag name for the configuration file path.
const configFlagName = "config"

// NewCommand returns a new download command that downloads and caches IBKR data.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	flags := newFlags()
	return &appcmd.Command{
		Use:   name,
		Short: "Download and cache IBKR data via Flex Query API",
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

func run(ctx context.Context, _ appext.Container, flags *flags) error {
	if flags.Config == "" {
		return appcmd.NewInvalidArgumentErrorf("--%s is required", configFlagName)
	}
	config, err := ibctlconfig.ReadConfig(flags.Config)
	if err != nil {
		return err
	}
	// Create the downloader with default clients and run the download pipeline.
	logger := slog.Default()
	downloader := ibctldownload.NewDownloader(
		config,
		ibctldownload.WithLogger(logger),
	)
	return downloader.Download(ctx)
}
