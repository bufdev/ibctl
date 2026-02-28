// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctlconfig provides configuration parsing and validation for ibctl.
package ibctlconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bufdev/ibctl/internal/pkg/cli"
)

// configTemplate is the default configuration file template with comments.
// yaml.v3 does not preserve comments, so we hardcode the template string.
const configTemplate = `# The configuration file version.
#
# Required. The only current valid version is v1.
version: v1
# The data directory.
#
# Required.
data: ~/Documents/ibctl
# IBKR Flex Query configuration.
#
# Required. Create a Flex Query at https://www.interactivebrokers.com
# under Performance & Reports > Flex Queries.
ibkr:
  # The Flex Web Service token.
  token: ""
  # The Flex Query ID.
  query_id: ""
# Symbol classification configuration.
#
# Optional. Adds category, type, and sector metadata to holdings output.
# symbols:
#   - name: NET
#     category: EQUITY
#     type: STOCK
#     sector: TECH
`

// ExternalConfig is the YAML-serializable configuration file structure.
type ExternalConfig struct {
	// Version is the configuration file version (must be "v1").
	Version string `yaml:"version"`
	// Data is the data directory path.
	Data string `yaml:"data"`
	// IBKR holds the Interactive Brokers Flex Query configuration.
	IBKR ExternalIBKRConfig `yaml:"ibkr"`
	// Symbols is the optional list of symbol classifications.
	Symbols []ExternalSymbolConfig `yaml:"symbols"`
}

// ExternalIBKRConfig holds IBKR-specific configuration.
type ExternalIBKRConfig struct {
	// Token is the Flex Web Service token.
	Token string `yaml:"token"`
	// QueryID is the Flex Query ID.
	QueryID string `yaml:"query_id"`
}

// ExternalSymbolConfig holds classification metadata for a symbol.
type ExternalSymbolConfig struct {
	// Name is the ticker symbol.
	Name string `yaml:"name"`
	// Category is the asset category (e.g., "EQUITY").
	Category string `yaml:"category"`
	// Type is the asset type (e.g., "STOCK", "ETF").
	Type string `yaml:"type"`
	// Sector is the sector classification (e.g., "TECH").
	Sector string `yaml:"sector"`
}

// Config is the validated runtime configuration.
type Config struct {
	// DataDirPath is the resolved absolute path to the data directory.
	DataDirPath string
	// IBKRToken is the Flex Web Service token.
	IBKRToken string
	// IBKRQueryID is the Flex Query ID.
	IBKRQueryID string
	// SymbolConfigs maps ticker symbols to their classification metadata.
	SymbolConfigs map[string]SymbolConfig
}

// SymbolConfig holds classification metadata for a symbol.
type SymbolConfig struct {
	// Category is the asset category (e.g., "EQUITY").
	Category string
	// Type is the asset type (e.g., "STOCK", "ETF").
	Type string
	// Sector is the sector classification (e.g., "TECH").
	Sector string
}

// NewConfig validates an ExternalConfig and returns a runtime Config.
func NewConfig(externalConfig ExternalConfig) (*Config, error) {
	if externalConfig.Version != "v1" {
		return nil, fmt.Errorf("unsupported config version %q, must be v1", externalConfig.Version)
	}
	if externalConfig.Data == "" {
		return nil, errors.New("data directory is required")
	}
	if externalConfig.IBKR.Token == "" {
		return nil, errors.New("ibkr.token is required")
	}
	if externalConfig.IBKR.QueryID == "" {
		return nil, errors.New("ibkr.query_id is required")
	}
	// Resolve home directory in the data path.
	dataDirPath, err := cli.ExpandHome(externalConfig.Data)
	if err != nil {
		return nil, err
	}
	// Build symbol configs map, checking for duplicates.
	symbolConfigs := make(map[string]SymbolConfig, len(externalConfig.Symbols))
	for _, s := range externalConfig.Symbols {
		if s.Name == "" {
			return nil, errors.New("symbol name is required")
		}
		if _, ok := symbolConfigs[s.Name]; ok {
			return nil, fmt.Errorf("duplicate symbol name %q", s.Name)
		}
		symbolConfigs[s.Name] = SymbolConfig{
			Category: s.Category,
			Type:     s.Type,
			Sector:   s.Sector,
		}
	}
	return &Config{
		DataDirPath:   dataDirPath,
		IBKRToken:     externalConfig.IBKR.Token,
		IBKRQueryID:   externalConfig.IBKR.QueryID,
		SymbolConfigs: symbolConfigs,
	}, nil
}

// DataDirV1Path returns the versioned data directory path.
func (c *Config) DataDirV1Path() string {
	return filepath.Join(c.DataDirPath, "v1")
}

// ReadExternalConfig reads and parses a YAML configuration file.
func ReadExternalConfig(filePath string) (ExternalConfig, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return ExternalConfig{}, fmt.Errorf("reading config file: %w", err)
	}
	var externalConfig ExternalConfig
	if err := cli.UnmarshalYAMLStrict(data, &externalConfig); err != nil {
		return ExternalConfig{}, fmt.Errorf("parsing config file: %w", err)
	}
	return externalConfig, nil
}

// InitConfig writes a default configuration template to the given file path.
// Returns an error if the file already exists.
func InitConfig(filePath string) error {
	if _, err := os.Stat(filePath); err == nil {
		return fmt.Errorf("config file already exists: %s", filePath)
	}
	return os.WriteFile(filePath, []byte(configTemplate), 0o644)
}

// ValidateConfigFile reads and validates a configuration file.
func ValidateConfigFile(filePath string) error {
	externalConfig, err := ReadExternalConfig(filePath)
	if err != nil {
		return err
	}
	_, err = NewConfig(externalConfig)
	return err
}

// ReadConfig reads, parses, and validates a configuration file, returning the runtime Config.
func ReadConfig(filePath string) (*Config, error) {
	externalConfig, err := ReadExternalConfig(filePath)
	if err != nil {
		return nil, err
	}
	return NewConfig(externalConfig)
}
