# Split data_dir into data_dir + cache_dir

## Context

`data_dir` currently holds both persistent accumulated data (trades) and blow-away-safe snapshots (positions, transfers, FX rates, etc.). Splitting into separate directories makes it clear what's safe to delete. Only `trades.json` accumulates over time (merging across 365-day download windows). Everything else is re-downloaded fresh on each run.

## Directory Layout

**`data_dir/accounts/<alias>/`** — persistent, accumulates:
- `trades.json` — incrementally merged, cannot be re-downloaded for dates > 365 days ago

**`cache_dir/accounts/<alias>/`** — blow-away safe, re-downloaded fresh:
- `positions.json` — snapshot, overwritten each download
- `transfers.json` — snapshot
- `trade_transfers.json` — snapshot
- `corporate_actions.json` — snapshot
- `cash_positions.json` — snapshot

**`cache_dir/fx/<BASE>.<QUOTE>/`** — blow-away safe:
- `rates.json` — eagerly re-downloaded from BoC/frankfurter

## Config Changes

**File**: `internal/ibctl/ibctlconfig/ibctlconfig.go`

New YAML field:
```yaml
# Cache directory for downloaded snapshots (positions, FX rates, etc.).
# Safe to delete — re-populated on next download.
# Required.
cache_dir: ~/Documents/ibkr/cache
```

New `ExternalConfigV1` field:
```go
CacheDir string `yaml:"cache_dir"`
```

`Config` struct changes:
- Add `CacheDirPath string` — resolved cache directory path
- Remove `DataDirV1Path` — replace with `DataDirPath` (no more /v1/)
- Remove `AccountsDirPath` and `FXDirPath` — these are implementation details computed by the packages that need them (download, merge, fxrates), not config concerns

## Download Changes

**File**: `internal/ibctl/ibctldownload/ibctldownload.go`

The downloader receives both `dataDirPath` and `cacheDirPath`. It computes subdirectory paths internally:
- `trades.json` → written to `dataDirPath/accounts/<alias>/`
- All other snapshot files → written to `cacheDirPath/accounts/<alias>/`
- FX rates → written to `cacheDirPath/fx/<pair>/`

## Merge Changes

**File**: `internal/ibctl/ibctlmerge/ibctlmerge.go`

`Merge` signature changes to accept both directory paths:
- `dataAccountsDirPath` (= `dataDirPath/accounts`) — for trades.json
- `cacheAccountsDirPath` (= `cacheDirPath/accounts`) — for positions, transfers, trade_transfers, corporate_actions, cash_positions

Callers compute the `accounts/` subdirectory before calling.

## Holdings/Command Changes

**File**: `cmd/ibctl/internal/command/holdings/holdingsoverview/holdingsoverview.go`

- FX store loads from `cacheDirPath/fx`
- Pass both account dir paths to merge

**File**: `cmd/ibctl/internal/ibctlcmd/ibctlcmd.go`

- NewDownloader receives both `dataDirPath` and `cacheDirPath` from config

## Files to Modify

1. `internal/ibctl/ibctlconfig/ibctlconfig.go` — add `cache_dir` field, new Config paths
2. `internal/ibctl/ibctldownload/ibctldownload.go` — split writes between data and cache dirs
3. `internal/ibctl/ibctlmerge/ibctlmerge.go` — accept two directory paths
4. `cmd/ibctl/internal/command/holdings/holdingsoverview/holdingsoverview.go` — use cache path for FX store
5. `cmd/ibctl/internal/ibctlcmd/ibctlcmd.go` — pass both paths to downloader
6. `~/Documents/ibkr/ibctl.yaml` — add cache_dir
7. `README.md` — update file locations and data directory docs

## Migration

Manual: move `data_dir/v1/accounts/<alias>/trades.json` to `data_dir/accounts/<alias>/trades.json`. Delete old `data_dir/v1/` tree. The next download populates `cache_dir`.

## Files to Modify

1. `internal/ibctl/ibctlconfig/ibctlconfig.go` — add `cache_dir`, change Config to `DataDirPath` + `CacheDirPath`, remove `AccountsDirPath`/`FXDirPath`/`DataDirV1Path`
2. `internal/ibctl/ibctldownload/ibctldownload.go` — accept both dir paths, split writes
3. `internal/ibctl/ibctlmerge/ibctlmerge.go` — accept two account dir paths
4. `internal/ibctl/ibctlfxrates/ibctlfxrates.go` — no change (already takes fxDirPath)
5. `cmd/ibctl/internal/ibctlcmd/ibctlcmd.go` — pass both paths from config
6. `cmd/ibctl/internal/command/holdings/holdingsoverview/holdingsoverview.go` — compute fx path from cache dir
7. `~/Documents/ibkr/ibctl.yaml` — add `cache_dir`
8. `README.md` — update docs

## Verification

1. `go build ./...` and `golangci-lint run ./...` pass
2. `ibctl download` writes trades to `data_dir/accounts/`, everything else to `cache_dir/accounts/` and `cache_dir/fx/`
3. `ibctl holdings overview --cached` still works reading from both dirs
4. Delete `cache_dir` entirely, run `ibctl download` — output matches previous run
5. `data_dir` only contains `accounts/<alias>/trades.json` files
