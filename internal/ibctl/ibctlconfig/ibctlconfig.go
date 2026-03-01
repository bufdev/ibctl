// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctlconfig provides configuration parsing and validation for ibctl.
package ibctlconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/bufdev/ibctl/internal/standard/xos"
	"gopkg.in/yaml.v3"
)

// DefaultConfigFileName is the default configuration file name in the current directory.
const DefaultConfigFileName = "ibctl.yaml"

// validAliasPattern matches lowercase alphanumeric strings with hyphens, used for account aliases.
var validAliasPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// configTemplate is the default configuration file template with comments.
// yaml.v3 does not preserve comments, so we hardcode the template string.
const configTemplate = `# The configuration file version.
#
# Required. The only current valid version is v1.
version: v1
# The data directory for ibctl to store downloaded and computed data.
#
# Required. A v1/ subdirectory will be created within this directory.
data_dir: ~/Documents/ibctl
# The Flex Query ID (visible next to your query name in the IBKR portal).
#
# Required. Create a Flex Query at https://www.interactivebrokers.com
# under Performance & Reports > Flex Queries. Include the Trades,
# Open Positions, and Cash Transactions sections with all fields enabled.
#
# The Flex Web Service token must be set via the IBKR_FLEX_WEB_SERVICE_TOKEN environment variable.
flex_query_id: ""
# Account aliases mapping.
#
# Required. Maps user-chosen aliases to IBKR account IDs.
# Account numbers are confidential — aliases are used in output and directory names.
# Aliases must be lowercase alphanumeric with hyphens (e.g., "rrsp", "hold-co").
accounts:
  # my-account: "U1234567"
# Directory containing IBKR Activity Statement CSVs for historical data.
#
# Required. Organize by account subdirectory (e.g., ~/Documents/ibkr-statements/my-account/).
# ibctl reads all *.csv files recursively. See README for setup instructions.
activity_statements_dir: ~/Documents/ibkr-statements
# Permanent seed data directory for pre-transfer tax lots from previous brokers.
#
# Optional. Organized by account subdirectory (e.g., seed_dir/individual/lots.json).
# This data is manually curated and must not be deleted.
# seed_dir: ~/Documents/ibkr/seed
# Symbol classification configuration.
#
# Optional. Adds category, type, and sector metadata to holdings output.
# symbols:
#   - name: NET
#     category: EQUITY
#     type: STOCK
#     sector: TECH
`

// ExternalConfigV1 is the YAML-serializable configuration file structure for version v1.
type ExternalConfigV1 struct {
	// Version is the configuration file version (must be "v1").
	Version string `yaml:"version"`
	// DataDir is the data directory for ibctl to store downloaded and computed data.
	DataDir string `yaml:"data_dir"`
	// FlexQueryID is the Flex Query ID.
	FlexQueryID string `yaml:"flex_query_id"`
	// ActivityStatementsDir is the directory containing IBKR Activity Statement CSVs.
	ActivityStatementsDir string `yaml:"activity_statements_dir"`
	// SeedDir is the permanent seed data directory for pre-transfer tax lots.
	// Organized by account subdirectory (e.g., seed_dir/individual/lots.json).
	// This data is manually curated and must not be deleted.
	SeedDir string `yaml:"seed_dir"`
	// Accounts maps user-chosen aliases to IBKR account IDs.
	Accounts map[string]string `yaml:"accounts"`
	// Symbols is the optional list of symbol classifications.
	Symbols []ExternalSymbolConfigV1 `yaml:"symbols"`
}

// ExternalSymbolConfigV1 holds classification metadata for a symbol in v1 config.
type ExternalSymbolConfigV1 struct {
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
	// DataDirV1Path is the resolved versioned data directory path (data_dir/v1).
	DataDirV1Path string
	// AccountsDirPath is the directory for per-account cached data (data_dir/v1/accounts).
	AccountsDirPath string
	// FXDirPath is the directory for FX rate data per currency pair (data_dir/v1/fx).
	FXDirPath string
	// IBKRFlexQueryID is the Flex Query ID.
	//
	// To create a Flex Query, log in to IBKR Client Portal, navigate to
	// Performance & Reports > Flex Queries, and create a new query with
	// Trades, Open Positions, Cash Transactions, Transfers, Trade Transfers,
	// and Corporate Actions sections enabled.
	// The Query ID is displayed next to the query name in the list.
	IBKRFlexQueryID string
	// ActivityStatementsDirPath is the resolved path to the Activity Statements directory.
	ActivityStatementsDirPath string
	// SeedDirPath is the resolved path to the permanent seed data directory.
	// Empty if not configured.
	SeedDirPath string
	// AccountAliases maps account aliases to IBKR account IDs (e.g., "rrsp" → "U1234567").
	AccountAliases map[string]string
	// AccountIDToAlias maps IBKR account IDs to aliases (e.g., "U1234567" → "rrsp").
	AccountIDToAlias map[string]string
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

// NewConfigV1 validates an ExternalConfigV1 and returns a runtime Config.
func NewConfigV1(externalConfig ExternalConfigV1) (*Config, error) {
	if externalConfig.Version != "v1" {
		return nil, fmt.Errorf("unsupported config version %q, must be v1", externalConfig.Version)
	}
	if externalConfig.DataDir == "" {
		return nil, errors.New("data_dir is required")
	}
	if externalConfig.FlexQueryID == "" {
		return nil, errors.New("flex_query_id is required")
	}
	if externalConfig.ActivityStatementsDir == "" {
		return nil, errors.New("activity_statements_dir is required")
	}
	if len(externalConfig.Accounts) == 0 {
		return nil, errors.New("accounts is required, must have at least one account alias mapping")
	}
	// Build account mappings, validating aliases and checking for duplicates.
	accountAliases := make(map[string]string, len(externalConfig.Accounts))
	accountIDToAlias := make(map[string]string, len(externalConfig.Accounts))
	for alias, accountID := range externalConfig.Accounts {
		if !validAliasPattern.MatchString(alias) {
			return nil, fmt.Errorf("account alias %q is invalid, must be lowercase alphanumeric with hyphens", alias)
		}
		if accountID == "" {
			return nil, fmt.Errorf("account ID for alias %q is required", alias)
		}
		if existingAlias, ok := accountIDToAlias[accountID]; ok {
			return nil, fmt.Errorf("duplicate account ID %q used by aliases %q and %q", accountID, existingAlias, alias)
		}
		accountAliases[alias] = accountID
		accountIDToAlias[accountID] = alias
	}
	// Resolve the data directory path and compute the v1 subdirectory.
	dataDirPath, err := xos.ExpandHome(externalConfig.DataDir)
	if err != nil {
		return nil, err
	}
	dataDirV1Path := filepath.Join(dataDirPath, "v1")
	// Resolve the activity statements directory path.
	activityStatementsDirPath, err := xos.ExpandHome(externalConfig.ActivityStatementsDir)
	if err != nil {
		return nil, err
	}
	// Resolve the seed data directory path if configured.
	var seedDirPath string
	if externalConfig.SeedDir != "" {
		seedDirPath, err = xos.ExpandHome(externalConfig.SeedDir)
		if err != nil {
			return nil, err
		}
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
		DataDirV1Path:             dataDirV1Path,
		AccountsDirPath:           filepath.Join(dataDirV1Path, "accounts"),
		FXDirPath:                 filepath.Join(dataDirV1Path, "fx"),
		IBKRFlexQueryID:           externalConfig.FlexQueryID,
		ActivityStatementsDirPath: activityStatementsDirPath,
		SeedDirPath:               seedDirPath,
		AccountAliases:            accountAliases,
		AccountIDToAlias:          accountIDToAlias,
		SymbolConfigs:             symbolConfigs,
	}, nil
}

// ReadConfig reads and validates a configuration file at the given path.
// Returns a clear error message directing users to run "ibctl config init" if the file is missing.
func ReadConfig(filePath string) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("configuration file not found at %s, run \"ibctl config init\" to create one", filePath)
		}
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	var externalConfig ExternalConfigV1
	if err := unmarshalYAMLStrict(data, &externalConfig); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", filePath, err)
	}
	config, err := NewConfigV1(externalConfig)
	if err != nil {
		return nil, fmt.Errorf("invalid configuration in %s: %w", filePath, err)
	}
	return config, nil
}

// InitConfig creates a new configuration file with a documented template at the given path.
// Returns an error if the file already exists.
func InitConfig(filePath string) error {
	if _, err := os.Stat(filePath); err == nil {
		return fmt.Errorf("configuration file already exists: %s", filePath)
	}
	// Create parent directories if they don't exist.
	if dir := filepath.Dir(filePath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating directory: %w", err)
		}
	}
	return os.WriteFile(filePath, []byte(configTemplate), 0o644)
}

// ValidateConfig reads and validates a configuration file at the given path.
func ValidateConfig(filePath string) error {
	_, err := ReadConfig(filePath)
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
