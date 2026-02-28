// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctlconfig provides configuration parsing and validation for ibctl.
//
// Configuration is stored at ~/.config/ibctl/config.yaml (or $IBCTL_CONFIG_DIR/config.yaml).
// Downloaded data is stored at ~/.local/share/ibctl/v1 (or $IBCTL_DATA_DIR/v1).
package ibctlconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ConfigFileName is the name of the configuration file within the config directory.
const ConfigFileName = "config.yaml"

// configTemplate is the default configuration file template with comments.
// yaml.v3 does not preserve comments, so we hardcode the template string.
const configTemplate = `# The configuration file version.
#
# Required. The only current valid version is v1.
version: v1
# IBKR Flex Query configuration.
#
# Required. Create a Flex Query at https://www.interactivebrokers.com
# under Performance & Reports > Flex Queries. Include the Trades,
# Open Positions, and Cash Transactions sections with all fields enabled.
#
# The Flex Web Service token must be set via the IBKR_TOKEN environment variable.
ibkr:
  # The Flex Query ID (visible next to your query name in the IBKR portal).
  #
  # Required.
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
	// IBKR holds the Interactive Brokers Flex Query configuration.
	IBKR ExternalIBKRConfig `yaml:"ibkr"`
	// Symbols is the optional list of symbol classifications.
	Symbols []ExternalSymbolConfig `yaml:"symbols"`
}

// ExternalIBKRConfig holds IBKR-specific configuration.
type ExternalIBKRConfig struct {
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

// Config is the validated runtime configuration derived from the config file.
type Config struct {
	// IBKRQueryID is the Flex Query ID.
	//
	// To create a Flex Query, log in to IBKR Client Portal, navigate to
	// Performance & Reports > Flex Queries, and create a new query with
	// Trades, Open Positions, and Cash Transactions sections enabled.
	// The Query ID is displayed next to the query name in the list.
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
	if externalConfig.IBKR.QueryID == "" {
		return nil, errors.New("ibkr.query_id is required")
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
		IBKRQueryID:   externalConfig.IBKR.QueryID,
		SymbolConfigs: symbolConfigs,
	}, nil
}

// ConfigFilePath returns the path to the configuration file within the given config directory.
func ConfigFilePath(configDirPath string) string {
	return filepath.Join(configDirPath, ConfigFileName)
}

// DataDirV1Path returns the versioned data directory path within the given data directory.
func DataDirV1Path(dataDirPath string) string {
	return filepath.Join(dataDirPath, "v1")
}

// ReadConfig reads and validates the configuration file from the given config directory.
// Returns a clear error message directing users to run "ibctl config init" if the file is missing.
func ReadConfig(configDirPath string) (*Config, error) {
	filePath := ConfigFilePath(configDirPath)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("configuration file not found at %s, run \"ibctl config init\" to create one", filePath)
		}
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	var externalConfig ExternalConfig
	if err := unmarshalYAMLStrict(data, &externalConfig); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", filePath, err)
	}
	return NewConfig(externalConfig)
}

// InitConfig creates a new configuration file with a documented template.
// Creates the config directory if it does not exist.
// Returns the path to the created file, or an error if the file already exists.
func InitConfig(configDirPath string) (string, error) {
	filePath := ConfigFilePath(configDirPath)
	if _, err := os.Stat(filePath); err == nil {
		return "", fmt.Errorf("configuration file already exists: %s", filePath)
	}
	// Create the config directory if it does not exist.
	if err := os.MkdirAll(configDirPath, 0o755); err != nil {
		return "", fmt.Errorf("creating config directory: %w", err)
	}
	if err := os.WriteFile(filePath, []byte(configTemplate), 0o644); err != nil {
		return "", err
	}
	return filePath, nil
}

// ValidateConfig reads and validates the configuration file from the given config directory.
func ValidateConfig(configDirPath string) error {
	_, err := ReadConfig(configDirPath)
	return err
}

// unmarshalYAMLStrict unmarshals the data as YAML with strict field checking.
// If the data length is 0, this is a no-op.
func unmarshalYAMLStrict(data []byte, v any) error {
	if len(data) == 0 {
		return nil
	}
	yamlDecoder := yaml.NewDecoder(bytes.NewReader(data))
	// Reject unknown fields.
	yamlDecoder.KnownFields(true)
	if err := yamlDecoder.Decode(v); err != nil {
		return fmt.Errorf("could not unmarshal as YAML: %w", err)
	}
	return nil
}
