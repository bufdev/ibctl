// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctlpath derives directory paths from the ibctl base directory.
// All subdirectory layout is defined here so callers don't duplicate
// path construction logic.
//
// The base directory (--dir flag) contains:
//
//	ibctl.yaml                        Config file
//	data/accounts/<alias>/            Persistent trade data
//	cache/accounts/<alias>/           Blow-away-safe snapshots
//	cache/fx/<BASE>.<QUOTE>/          FX rate data
//	activity_statements/<alias>/      User-managed Activity Statement CSVs
//	seed/<alias>/                     Optional pre-transfer tax lots
package ibctlpath

import "path/filepath"

// ConfigFileName is the well-known config file name within the base directory.
const ConfigFileName = "ibctl.yaml"

// ConfigFilePath returns the path to the config file within the base directory.
func ConfigFilePath(dirPath string) string {
	return filepath.Join(dirPath, ConfigFileName)
}

// DataAccountsDirPath returns the directory for persistent per-account trade data.
func DataAccountsDirPath(dirPath string) string {
	return filepath.Join(dirPath, "data", "accounts")
}

// DataAccountDirPath returns the directory for a specific account's persistent trade data.
func DataAccountDirPath(dirPath string, alias string) string {
	return filepath.Join(dirPath, "data", "accounts", alias)
}

// CacheAccountsDirPath returns the directory for cached per-account snapshot data.
func CacheAccountsDirPath(dirPath string) string {
	return filepath.Join(dirPath, "cache", "accounts")
}

// CacheAccountDirPath returns the directory for a specific account's cached snapshot data.
func CacheAccountDirPath(dirPath string, alias string) string {
	return filepath.Join(dirPath, "cache", "accounts", alias)
}

// CacheFXDirPath returns the directory for cached FX rate data.
func CacheFXDirPath(dirPath string) string {
	return filepath.Join(dirPath, "cache", "fx")
}

// ActivityStatementsDirPath returns the directory for Activity Statement CSVs.
func ActivityStatementsDirPath(dirPath string) string {
	return filepath.Join(dirPath, "activity_statements")
}

// SeedDirPath returns the directory for permanent seed data from previous brokers.
func SeedDirPath(dirPath string) string {
	return filepath.Join(dirPath, "seed")
}
