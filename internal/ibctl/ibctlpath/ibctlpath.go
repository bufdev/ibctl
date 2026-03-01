// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctlpath provides derived directory paths from the data and cache
// directory roots. Centralizes the subdirectory layout so callers don't
// duplicate filepath.Join logic.
package ibctlpath

import "path/filepath"

// DataAccountsDirPath returns the directory for persistent per-account trade data.
func DataAccountsDirPath(dataDirPath string) string {
	return filepath.Join(dataDirPath, "accounts")
}

// DataAccountDirPath returns the directory for a specific account's persistent trade data.
func DataAccountDirPath(dataDirPath string, alias string) string {
	return filepath.Join(dataDirPath, "accounts", alias)
}

// CacheAccountsDirPath returns the directory for cached per-account snapshot data.
func CacheAccountsDirPath(cacheDirPath string) string {
	return filepath.Join(cacheDirPath, "accounts")
}

// CacheAccountDirPath returns the directory for a specific account's cached snapshot data.
func CacheAccountDirPath(cacheDirPath string, alias string) string {
	return filepath.Join(cacheDirPath, "accounts", alias)
}

// CacheFXDirPath returns the directory for cached FX rate data.
func CacheFXDirPath(cacheDirPath string) string {
	return filepath.Join(cacheDirPath, "fx")
}
