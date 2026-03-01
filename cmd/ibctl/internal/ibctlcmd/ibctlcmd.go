// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctlcmd provides shared wiring for ibctl commands.
package ibctlcmd

import (
	"errors"

	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/bufdev/ibctl/internal/ibctl/ibctldownload"
	"github.com/bufdev/ibctl/internal/pkg/bankofcanada"
	"github.com/bufdev/ibctl/internal/pkg/frankfurter"
	"github.com/bufdev/ibctl/internal/pkg/ibkrflexquery"
)

const (
	// ConfigFlagName is the flag name for the configuration file path.
	ConfigFlagName = "config"
	// ibkrFlexWebServiceTokenEnvVar is the environment variable name for the IBKR Flex Web Service token.
	ibkrFlexWebServiceTokenEnvVar = "IBKR_FLEX_WEB_SERVICE_TOKEN"
)

// NewDownloader constructs a Downloader by reading the config file, extracting the
// IBKR token from the environment, and creating the required API clients.
func NewDownloader(container appext.Container, configFilePath string) (ibctldownload.Downloader, error) {
	// Read and validate the configuration file.
	config, err := ibctlconfig.ReadConfig(configFilePath)
	if err != nil {
		return nil, err
	}
	// Read the IBKR token from the environment via the app container.
	ibkrToken := container.Env(ibkrFlexWebServiceTokenEnvVar)
	if ibkrToken == "" {
		return nil, errors.New(ibkrFlexWebServiceTokenEnvVar + " environment variable is required, set it to your IBKR Flex Web Service token (see \"ibctl --help\" for details)")
	}
	// Use the data directory from config.
	dataDirV1Path := config.DataDirV1Path
	// Extract the logger from the appext container.
	logger := container.Logger()
	// Construct the API clients.
	flexQueryClient := ibkrflexquery.NewClient(logger)
	fxRateClient := frankfurter.NewClient()
	bocClient := bankofcanada.NewClient()
	return ibctldownload.NewDownloader(logger, ibkrToken, dataDirV1Path, config, flexQueryClient, fxRateClient, bocClient), nil
}
