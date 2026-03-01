# Convention-based directory layout with --dir flag

## Context

Currently, commands take `--config` pointing to an ibctl.yaml that contains paths to data_dir, cache_dir, activity_statements_dir, and seed_dir. Since the user already made these relative paths within a single base directory, we can simplify: commands take `--dir` pointing to a base directory with a well-known layout. The directory paths are no longer config — they're conventions.

## Directory Layout

```
<dir>/                              # --dir flag, defaults to current directory
├── ibctl.yaml                      # Config file (flex_query_id, accounts, symbols)
├── data/                           # Persistent trade data — do not delete
│   └── accounts/<alias>/
│       └── trades.json
├── cache/                          # Blow-away safe — re-populated on download
│   ├── accounts/<alias>/
│   │   ├── positions.json
│   │   ├── transfers.json
│   │   ├── trade_transfers.json
│   │   ├── corporate_actions.json
│   │   └── cash_positions.json
│   └── fx/<BASE>.<QUOTE>/
│       └── rates.json
├── activity_statements/            # User-managed Activity Statement CSVs
│   └── <alias>/*.csv
└── seed/                           # Optional — pre-transfer tax lots from previous brokers
    └── <alias>/transactions.json
```

## Config Changes

**File**: `internal/ibctl/ibctlconfig/ibctlconfig.go`

Remove from `ExternalConfigV1`: `DataDir`, `CacheDir`, `ActivityStatementsDir`, `SeedDir`.

Remove from `Config`: `DataDirPath`, `CacheDirPath`, `ActivityStatementsDirPath`, `SeedDirPath`.

Add to `Config`: `DirPath string` — the resolved base directory.

`NewConfigV1` signature changes to `NewConfigV1(externalConfig ExternalConfigV1, dirPath string)`. The `dirPath` is the resolved `--dir` value. All subdirectory paths are derived via `ibctlpath`.

`ReadConfig` changes to `ReadConfig(dirPath string)` — always reads `<dirPath>/ibctl.yaml`.

Remove `resolveConfigPath` — no longer needed since paths are not in config.

Update config template to remove directory fields.

## ibctlpath Changes

**File**: `internal/ibctl/ibctlpath/ibctlpath.go`

Add functions that derive all paths from the base dir:
- `ConfigFilePath(dirPath) string` → `<dir>/ibctl.yaml`
- `DataAccountsDirPath(dirPath)` → `<dir>/data/accounts`
- `DataAccountDirPath(dirPath, alias)` → `<dir>/data/accounts/<alias>`
- `CacheAccountsDirPath(dirPath)` → `<dir>/cache/accounts`
- `CacheAccountDirPath(dirPath, alias)` → `<dir>/cache/accounts/<alias>`
- `CacheFXDirPath(dirPath)` → `<dir>/cache/fx`
- `ActivityStatementsDirPath(dirPath)` → `<dir>/activity_statements`
- `SeedDirPath(dirPath)` → `<dir>/seed`

Remove the separate `dataDirPath`/`cacheDirPath` parameters from existing functions — everything derives from the single base dir.

## Command Changes

**File**: `cmd/ibctl/internal/ibctlcmd/ibctlcmd.go`

- Rename `ConfigFlagName` to `DirFlagName`, value `"dir"`.
- `NewDownloader` takes `dirPath string` instead of `configFilePath string`. Reads config via `ibctlconfig.ReadConfig(dirPath)`.

All command files (`holdingsoverview.go`, `download.go`, `probe.go`, `configinit.go`, `configedit.go`, `configvalidate.go`):
- Replace `--config` flag with `--dir` flag, default `.` (current directory).
- Pass `flags.Dir` to `NewDownloader` / `ReadConfig`.

## Download Changes

**File**: `internal/ibctl/ibctldownload/ibctldownload.go`

Replace `config.DataDirPath` / `config.CacheDirPath` with `ibctlpath` calls using `config.DirPath`.

## Merge Changes

**File**: `internal/ibctl/ibctlmerge/ibctlmerge.go`

Callers pass paths derived from `config.DirPath` via `ibctlpath`.

## Holdings Changes

**File**: `cmd/ibctl/internal/command/holdings/holdingsoverview/holdingsoverview.go`

Use `ibctlpath.CacheFXDirPath(config.DirPath)` for FX store.
Use `ibctlpath` for merge calls.

## Config Init

`ibctl config init --dir .` creates `./ibctl.yaml` with the simplified template (no directory fields).

## ibctl.yaml Simplification

The config file shrinks to:
```yaml
version: v1
flex_query_id: "1419229"
accounts:
  rrsp: U22473342
  holdco: U22980581
  individual: U6014632
symbols:
  - name: NET
    category: EQUITY
    type: STOCK
    sector: TECH
    geo: US
```

No directory paths in config at all.

## Files to Modify

1. `internal/ibctl/ibctlconfig/ibctlconfig.go` — remove dir fields, add DirPath, change ReadConfig/NewConfigV1
2. `internal/ibctl/ibctlpath/ibctlpath.go` — add ConfigFilePath, ActivityStatementsDirPath, SeedDirPath; change all functions to derive from single base dir
3. `internal/ibctl/ibctldownload/ibctldownload.go` — use config.DirPath + ibctlpath
4. `internal/ibctl/ibctlmerge/ibctlmerge.go` — no changes (callers pass the right paths)
5. `cmd/ibctl/internal/ibctlcmd/ibctlcmd.go` — rename to DirFlagName, update NewDownloader
6. `cmd/ibctl/internal/command/holdings/holdingsoverview/holdingsoverview.go` — --dir flag
7. `cmd/ibctl/internal/command/download/download.go` — --dir flag
8. `cmd/ibctl/internal/command/probe/probe.go` — --dir flag
9. `cmd/ibctl/internal/command/config/configinit/configinit.go` — --dir flag
10. `cmd/ibctl/internal/command/config/configedit/configedit.go` — --dir flag
11. `cmd/ibctl/internal/command/config/configvalidate/configvalidate.go` — --dir flag
12. `~/Documents/ibkr/ibctl.yaml` — remove directory fields
13. `README.md` — update directory layout docs, --dir flag usage

## Verification

1. `go build ./...` and `golangci-lint run ./...` pass
2. `cd ~/Documents/ibkr && ibctl holdings overview --cached` works (--dir defaults to `.`)
3. `ibctl holdings overview --cached --dir ~/Documents/ibkr` works from any directory
4. `ibctl config init --dir /tmp/test` creates `/tmp/test/ibctl.yaml` without directory fields
5. Output matches previous run
