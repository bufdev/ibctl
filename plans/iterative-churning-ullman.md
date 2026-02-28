# Download Rework: Windowed API Calls + Implicit Download

## Context

IBKR's Flex Query API limits each request to 365 calendar days. To capture full account history, ibctl needs to make multiple API calls with sliding date windows and merge the results. Additionally, `ibctl download` should be a pre-caching mechanism — other commands like `holdings overview` should implicitly trigger a download when data is missing.

The API supports `fd` (from date) and `td` (to date) query parameters on the SendRequest endpoint to override the configured period.

## Changes

### 1. ibkrflexquery: Add date parameters to Download

**File**: `internal/pkg/ibkrflexquery/ibkrflexquery.go`

Add `fromDate` and `toDate` string params (format `YYYYMMDD`) to the `Download` method. Empty strings mean "use the query's configured period."

```go
// Client interface change.
Download(ctx context.Context, token string, queryID string, fromDate string, toDate string) (*FlexStatement, error)
```

In `sendRequest`, conditionally append `&fd=...&td=...` to the URL when both are non-empty.

Validate: token and queryID required (already done). fromDate/toDate: if one is set, both must be set.

### 2. ibctldownload: Windowed multi-call download with merge

**File**: `internal/ibctl/ibctldownload/ibctldownload.go`

Replace the single `flexQueryClient.Download()` call with a loop:

**Window strategy**: Start from today, go backwards in 365-day chunks.
- Window 0: `today - 364` to `today`
- Window 1: `today - 729` to `today - 365`
- Window 2: `today - 1094` to `today - 730`
- etc.

**Termination**: Stop when a window returns zero trades. This means we've gone past the beginning of the account. Simple and reliable — the only edge case is a >365-day gap in trading, which is extremely unlikely for an active account.

**Merge logic**:
- **Trades**: Accumulate across all windows. Deduplicate by `TradeID`. This is the core data that needs full history.
- **Positions**: Take from the most recent window only (window 0). Positions are a point-in-time snapshot of current holdings.
- **Cash transactions**: Accumulate across all windows for FX rate extraction. Deduplicate by date+currency.

After merging, the rest of the pipeline continues as before: convert to protos, compute tax lots, verify positions, write files.

### 3. Implicit download in commands that need data

**Files**: `cmd/ibctl/internal/command/holdings/holdingsoverview/holdingsoverview.go`, `cmd/ibctl/internal/command/download/download.go`

Add `Downloader.EnsureDownloaded(ctx) error` to the interface — checks if required data files exist and downloads if they don't.

The holdings overview command constructs a Downloader (same wiring as download command — needs IBKR_TOKEN, config, logger, clients) and calls `EnsureDownloaded` before reading data.

Extract the download wiring (reading config, getting token, constructing clients, creating downloader) into a shared helper to avoid duplication between the download and holdings commands. Place it in a shared command-layer helper file or inline it — keep it at the `cmd/` layer since it touches `appext.Container`.

`ibctl download` calls `Download()` directly (always re-downloads). `ibctl holdings overview` calls `EnsureDownloaded()` (downloads only if files are missing).

### 4. README update

**File**: `README.md`

- Remove the note about running `ibctl download` multiple times — ibctl handles windowing automatically
- Clarify that `ibctl download` pre-caches data, and other commands trigger download implicitly when needed
- Keep the IBKR setup instructions: user creates ONE query with 365-day period, ibctl handles the rest

### 5. Files to check for data existence

The minimum files needed for holdings overview: `trades.json` and `positions.json` in the v1 data directory. If either is missing, trigger download.

## File Manifest

**Modify**:
- `internal/pkg/ibkrflexquery/ibkrflexquery.go` — Add fromDate/toDate to Download signature and URL construction
- `internal/ibctl/ibctldownload/ibctldownload.go` — Windowed loop, merge/dedup, EnsureDownloaded
- `cmd/ibctl/internal/command/download/download.go` — Pass empty dates (or update call), extract shared wiring
- `cmd/ibctl/internal/command/holdings/holdingsoverview/holdingsoverview.go` — Construct downloader, call EnsureDownloaded before reading
- `README.md` — Update download docs

## Verification

1. `make generate && make all` — 0 issues, all tests pass
2. `ibctl --help` — shows updated command descriptions
3. Code review: windowing terminates when zero trades returned, trades deduped by ID, positions from latest window only
