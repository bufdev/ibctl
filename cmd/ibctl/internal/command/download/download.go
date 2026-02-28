// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package download implements the "download" command.
package download

import (
	"context"
	"errors"

	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/bufdev/ibctl/internal/ibctl/ibctldownload"
	"github.com/bufdev/ibctl/internal/pkg/frankfurter"
	"github.com/bufdev/ibctl/internal/pkg/ibkrflexquery"
)

// ibkrTokenEnvVar is the environment variable name for the IBKR Flex Web Service token.
const ibkrTokenEnvVar = "IBKR_TOKEN"

// NewCommand returns a new download command that downloads and caches IBKR data.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	return &appcmd.Command{
		Use:   name,
		Short: "Download and cache IBKR data via Flex Query API",
		Args:  appcmd.NoArgs,
		Run: builder.NewRunFunc(
			func(ctx context.Context, container appext.Container) error {
				return run(ctx, container)
			},
		),
	}
}

func run(ctx context.Context, container appext.Container) error {
	// Read and validate the configuration file.
	config, err := ibctlconfig.ReadConfig(container.ConfigDirPath())
	if err != nil {
		return err
	}
	// Read the IBKR token from the environment via the app container.
	ibkrToken := container.Env(ibkrTokenEnvVar)
	if ibkrToken == "" {
		return errors.New("IBKR_TOKEN environment variable is required")
	}
	// Compute the versioned data directory path.
	dataDirV1Path := ibctlconfig.DataDirV1Path(container.DataDirPath())
	// Extract the logger from the appext container.
	logger := container.Logger()
	// Construct the IBKR Flex Query client with the logger.
	flexQueryClient := ibkrflexquery.NewClient(logger)
	// Construct the FX rate client.
	fxRateClient := frankfurter.NewClient()
	// Create the downloader with all required dependencies.
	downloader := ibctldownload.NewDownloader(logger, ibkrToken, dataDirV1Path, config, flexQueryClient, fxRateClient)
	return downloader.Download(ctx)
}
