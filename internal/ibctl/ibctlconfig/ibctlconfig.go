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

	"github.com/bufdev/ibctl/internal/ibctl/ibctlpath"
	"github.com/bufdev/ibctl/internal/pkg/mathpb"
	"gopkg.in/yaml.v3"
)

// validAliasPattern matches lowercase alphanumeric strings with hyphens, used for account aliases.
var validAliasPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// configTemplate is the default configuration file template with comments.
// yaml.v3 does not preserve comments, so we hardcode the template string.
const configTemplate = `# The configuration file version.
#
# Required. The only current valid version is v1.
version: v1
# The Flex Query ID (visible next to your query name in the IBKR portal).
#
# Required. Create a Flex Query at https://www.interactivebrokers.com
# under Performance & Reports > Flex Queries. Include the Trades,
# Open Positions, Cash Transactions, Cash Report, Transfers,
# Trade Transfers, and Corporate Actions sections with all fields enabled.
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
# Symbol classification configuration.
#
# Optional. Adds category, type, sector, and geo metadata to holdings output.
# symbols:
#   - name: NET
#     category: EQUITY
#     type: STOCK
#     sector: TECH
#     geo: US
`

// ExternalConfigV1 is the YAML-serializable configuration file structure for version v1.
type ExternalConfigV1 struct {
	// Version is the configuration file version (must be "v1").
	Version string `yaml:"version"`
	// FlexQueryID is the Flex Query ID.
	FlexQueryID string `yaml:"flex_query_id"`
	// Accounts maps user-chosen aliases to IBKR account IDs.
	Accounts map[string]string `yaml:"accounts"`
	// Symbols is the optional list of symbol classifications.
	Symbols []ExternalSymbolConfigV1 `yaml:"symbols"`
	// Adjustments maps currency codes to manual cash adjustments (positive or negative).
	// Applied to cash positions in the holdings display.
	Adjustments map[string]string `yaml:"adjustments"`
	// Taxes configures capital gains tax rates for portfolio value computation.
	Taxes *ExternalTaxConfigV1 `yaml:"taxes"`
}

// ExternalTaxConfigV1 holds capital gains tax rate configuration.
type ExternalTaxConfigV1 struct {
	// STCG is the short-term capital gains tax rate (e.g., 0.408 for 40.8%).
	STCG float64 `yaml:"stcg"`
	// LTCG is the long-term capital gains tax rate (e.g., 0.28 for 28%).
	LTCG float64 `yaml:"ltcg"`
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
	// Geo is the geographic classification (e.g., "US", "INTL").
	Geo string `yaml:"geo"`
}

// Config is the validated runtime configuration derived from the config file.
type Config struct {
	// DirPath is the resolved base directory path (from --dir flag).
	// All subdirectory paths are derived from this via ibctlpath.
	DirPath string
	// IBKRFlexQueryID is the Flex Query ID.
	IBKRFlexQueryID string
	// AccountAliases maps account aliases to IBKR account IDs (e.g., "rrsp" → "U1234567").
	AccountAliases map[string]string
	// AccountIDToAlias maps IBKR account IDs to aliases (e.g., "U1234567" → "rrsp").
	AccountIDToAlias map[string]string
	// SymbolConfigs maps ticker symbols to their classification metadata.
	SymbolConfigs map[string]SymbolConfig
	// CashAdjustments maps currency codes to manual cash adjustments in micros.
	// Applied to cash positions in the holdings display.
	CashAdjustments map[string]int64
	// TaxRateSTCG is the short-term capital gains tax rate (e.g., 0.408).
	TaxRateSTCG float64
	// TaxRateLTCG is the long-term capital gains tax rate (e.g., 0.28).
	TaxRateLTCG float64
}

// SymbolConfig holds classification metadata for a symbol.
type SymbolConfig struct {
	// Category is the asset category (e.g., "EQUITY").
	Category string
	// Type is the asset type (e.g., "STOCK", "ETF").
	Type string
	// Sector is the sector classification (e.g., "TECH").
	Sector string
	// Geo is the geographic classification (e.g., "US", "INTL").
	Geo string
}

// NewConfigV1 validates an ExternalConfigV1 and returns a runtime Config.
// The dirPath is the resolved base directory (from --dir flag).
func NewConfigV1(externalConfig ExternalConfigV1, dirPath string) (*Config, error) {
	if externalConfig.Version != "v1" {
		return nil, fmt.Errorf("unsupported config version %q, must be v1", externalConfig.Version)
	}
	if externalConfig.FlexQueryID == "" {
		return nil, errors.New("flex_query_id is required")
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
			Geo:      s.Geo,
		}
	}
	// Parse cash adjustments, validating currency codes and decimal values.
	cashAdjustments := make(map[string]int64, len(externalConfig.Adjustments))
	for currency, value := range externalConfig.Adjustments {
		units, micros, err := mathpb.ParseToUnitsMicros(value)
		if err != nil {
			return nil, fmt.Errorf("invalid adjustment for %s: %w", currency, err)
		}
		cashAdjustments[currency] = units*1_000_000 + micros
	}
	// Extract tax rates if configured.
	var taxRateSTCG, taxRateLTCG float64
	if externalConfig.Taxes != nil {
		taxRateSTCG = externalConfig.Taxes.STCG
		taxRateLTCG = externalConfig.Taxes.LTCG
	}
	return &Config{
		DirPath:          dirPath,
		IBKRFlexQueryID:  externalConfig.FlexQueryID,
		AccountAliases:   accountAliases,
		AccountIDToAlias: accountIDToAlias,
		SymbolConfigs:    symbolConfigs,
		CashAdjustments:  cashAdjustments,
		TaxRateSTCG:      taxRateSTCG,
		TaxRateLTCG:      taxRateLTCG,
	}, nil
}

// ReadConfig reads and validates the configuration file from the base directory.
// The dirPath is the base directory containing ibctl.yaml.
func ReadConfig(dirPath string) (*Config, error) {
	// Resolve to absolute path for consistent behavior.
	absDirPath, err := filepath.Abs(dirPath)
	if err != nil {
		return nil, fmt.Errorf("resolving directory path: %w", err)
	}
	configFilePath := ibctlpath.ConfigFilePath(absDirPath)
	data, err := os.ReadFile(configFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("configuration file not found at %s, run \"ibctl config init\" to create one", configFilePath)
		}
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	var externalConfig ExternalConfigV1
	if err := unmarshalYAMLStrict(data, &externalConfig); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", configFilePath, err)
	}
	config, err := NewConfigV1(externalConfig, absDirPath)
	if err != nil {
		return nil, fmt.Errorf("invalid configuration in %s: %w", configFilePath, err)
	}
	return config, nil
}

// InitConfig creates a new configuration file with a documented template in the base directory.
// Returns an error if the file already exists.
func InitConfig(dirPath string) error {
	configFilePath := ibctlpath.ConfigFilePath(dirPath)
	if _, err := os.Stat(configFilePath); err == nil {
		return fmt.Errorf("configuration file already exists: %s", configFilePath)
	}
	// Create the directory if it doesn't exist.
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	return os.WriteFile(configFilePath, []byte(configTemplate), 0o644)
}

// ValidateConfig reads and validates the configuration file in the base directory.
func ValidateConfig(dirPath string) error {
	_, err := ReadConfig(dirPath)
	return err
}

// *** PRIVATE ***

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
