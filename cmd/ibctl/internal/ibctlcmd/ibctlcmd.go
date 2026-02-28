// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctlcmd provides shared wiring for ibctl commands that need
// the download pipeline (reading config, getting the IBKR token, constructing clients).
package ibctlcmd

import (
	"errors"

	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/bufdev/ibctl/internal/ibctl/ibctldownload"
	"github.com/bufdev/ibctl/internal/pkg/frankfurter"
	"github.com/bufdev/ibctl/internal/pkg/ibkrflexquery"
)

// ibkrTokenEnvVar is the environment variable name for the IBKR Flex Web Service token.
const ibkrTokenEnvVar = "IBKR_TOKEN"

// NewDownloader constructs a Downloader from the appext container by reading the
// config file, extracting the IBKR token from the environment, and creating the
// required API clients.
func NewDownloader(container appext.Container) (ibctldownload.Downloader, error) {
	// Read and validate the configuration file.
	config, err := ibctlconfig.ReadConfig(container.ConfigDirPath())
	if err != nil {
		return nil, err
	}
	// Read the IBKR token from the environment via the app container.
	ibkrToken := container.Env(ibkrTokenEnvVar)
	if ibkrToken == "" {
		return nil, errors.New("IBKR_TOKEN environment variable is required, set it to your IBKR Flex Web Service token (see \"ibctl --help\" for details)")
	}
	// Compute the versioned data directory path.
	dataDirV1Path := ibctlconfig.DataDirV1Path(container.DataDirPath())
	// Extract the logger from the appext container.
	logger := container.Logger()
	// Construct the API clients.
	flexQueryClient := ibkrflexquery.NewClient(logger)
	fxRateClient := frankfurter.NewClient()
	return ibctldownload.NewDownloader(logger, ibkrToken, dataDirV1Path, config, flexQueryClient, fxRateClient), nil
}
