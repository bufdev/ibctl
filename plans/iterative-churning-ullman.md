# Multi-Account Support, Transfers, and Corporate Actions

## Context

`ibctl holdings overview` produces incorrect results due to three root causes:

1. **Multi-account duplication**: IBKR returns one `<FlexStatement>` per account. Our parser captures only one (single struct, not slice). With 3 accounts, data from 2 accounts is lost or merged incorrectly — causing 2x quantity errors.

2. **Missing transfer-in data**: Positions transferred into IBKR (ACATS) have no buy trades. IBKR has a `Transfers` section and `TradeTransfers` section we don't request. Without these, FIFO can't match sells to buys for transferred positions.

3. **Missing corporate actions**: Stock splits, mergers, spinoffs change lot quantities without corresponding trades. IBKR has a `CorporateActions` section we don't request.

The user must also update their Flex Query in the IBKR portal to add Transfers, Incoming/Outgoing Trade Transfers, and Corporate Actions sections (all fields, same date/time/separator config as existing sections).

## Config

**File**: `internal/ibctl/ibctlconfig/ibctlconfig.go`

Add `accounts` to `ExternalConfigV1` — a map from alias to IBKR account ID. Required, at least one entry. Validate no duplicate account IDs, aliases are valid directory names (lowercase alphanumeric + hyphen).

```yaml
accounts:
  rrsp: "U1234567"
  holdco: "U2345678"
  individual: "U3456789"
```

Add to runtime `Config`:
- `AccountAliases map[string]string` (alias → account ID)
- `AccountIDToAlias map[string]string` (account ID → alias, built from the above)

Update `configTemplate` to include commented `accounts` section.

## Proto Changes

### Add `account_id` field to existing messages

- `trade.proto`: `string account_id = 14 [(buf.validate.field).required = true];`
- `position.proto`: `string account_id = 10 [(buf.validate.field).required = true];`
- `taxlot.proto` `TaxLot`: `string account_id = 6 [(buf.validate.field).required = true];`
- `taxlot.proto` `ComputedPosition`: `string account_id = 5 [(buf.validate.field).required = true];`
- `exchange_rate.proto`: no change (rates are global)

### New proto: `transfer.proto`

- `TransferType` enum: ACATS, ATON, FOP, INTERNAL
- `TransferDirection` enum: IN, OUT
- `Transfer` message: account_id, type, direction, symbol, date, quantity, transfer_price, currency_code, description

### New proto: `corporate_action.proto`

- `CorporateActionType` enum: FORWARD_SPLIT, REVERSE_SPLIT, MERGER, SPINOFF
- `CorporateAction` message: account_id, type, date, symbol, quantity, amount, currency_code, action_description, asset_category

Run `buf format -w && buf lint && make generate` after all proto changes.

## XML Parsing

**File**: `internal/pkg/ibkrflexquery/ibkrflexquery.go`

### Multi-account

Change `flexStatements.FlexStatement` from single struct to slice `[]FlexStatement`.

Add `AccountId string` (XML attribute `accountId`) to `FlexStatement`.

Change `Client.Download` return type from `*FlexStatement` to `[]FlexStatement`.

### New XML types

Add `XMLTransfer` struct: type, direction, symbol, dateTime, quantity, transferPrice, currency, description, accountId.

Add `XMLTradeTransfer` struct: symbol, dateTime, quantity, origTradePrice, origTradeDate, origTradeID, cost, holdingPeriodDateTime, currency, accountId.

Add `XMLCorporateAction` struct: type, symbol, dateTime, quantity, amount, currency, actionDescription, assetCategory, accountId.

Add these to `FlexStatement`:
```go
Transfers        []XMLTransfer        `xml:"Transfers>Transfer"`
TradeTransfers   []XMLTradeTransfer   `xml:"TradeTransfers>TradeTransfer"`
CorporateActions []XMLCorporateAction `xml:"CorporateActions>CorporateAction"`
```

## Download

**File**: `internal/ibctl/ibctldownload/ibctldownload.go`

- Accept account mappings (from config) in constructor
- Iterate over returned `[]FlexStatement`, look up alias from `accountIDToAlias[statement.AccountId]`
- Per-account storage: `v1/<alias>/trades.json`, `v1/<alias>/positions.json`, `v1/<alias>/transfers.json`, `v1/<alias>/corporate_actions.json`
- Exchange rates remain shared at `v1/exchange_rates.json`
- Set `account_id` (alias) on every proto during conversion
- Add conversion functions for transfers and corporate actions
- Add merge-with-cache functions for new types (same pattern as existing: dedup by composite key)
- Update `EnsureDownloaded` to check per-account paths
- Log per-account counts

If old flat `v1/trades.json` exists but per-account dirs don't, log warning telling user to re-download.

## Merge

**File**: `internal/ibctl/ibctlmerge/ibctlmerge.go`

- Update `MergedData` to include `Transfers`, `CorporateActions`
- Read from `v1/<alias>/` per account instead of `v1/`
- Accept account aliases to know which accounts exist
- Map Activity Statement CSV subdirectories to account aliases
- Set `account_id` on CSV-derived trades/positions
- Merge all accounts into unified sorted lists

## Tax Lots

**File**: `internal/ibctl/ibctltaxlot/ibctltaxlot.go`

This is the most complex change.

### Pre-processing

- Convert transfer-in records to synthetic BUY trades (transfer price as cost basis, transfer date as open date)
- Convert transfer-out records to synthetic SELL trades
- For `TradeTransfers`: use `orig_trade_date` as trade date (preserves holding period), `cost`/`orig_trade_price` as cost basis

### FIFO algorithm changes

- Group by `(account_id, symbol)` instead of just `symbol`
- Process all events (trades + corporate actions) in chronological order
- Corporate action handling:
  - **Forward split (FS)**: Multiply all open lot quantities by ratio, divide cost basis by ratio. Parse ratio from `actionDescription` (e.g., "SPLIT 4 FOR 1").
  - **Reverse split (RS)**: Inverse of forward split.
  - **Merger (TC)**: Close all lots of old symbol, create new lots of new symbol preserving cost basis.
  - **Spinoff (SO)**: Allocate fraction of cost basis to new symbol based on amount/value ratio.

### Updated signatures

`ComputeTaxLots` accepts trades + transfers + corporate actions.

`ComputePositions` groups by `(account_id, symbol)`, sets `account_id` on results.

`VerifyPositions` compares per `(account_id, symbol)`.

## Holdings

**File**: `internal/ibctl/ibctlholdings/ibctlholdings.go`

- Add `AccountAlias` to `HoldingOverview`
- Default display: **combined** (aggregate across accounts). Use `--account <alias>` for single-account view.
- `GetHoldingsOverview` accepts transfers + corporate actions, passes them to `ComputeTaxLots`
- For combined view: group computed positions by symbol (across accounts), sum quantities, weighted-average cost basis
- Add `ACCOUNT` column to headers (shown with `--account`, hidden in combined mode)

**File**: `cmd/ibctl/internal/command/holdings/holdingsoverview/holdingsoverview.go`

- Add `--account` flag (optional, filters to single account alias)
- Pass transfers/corporate actions from merged data through to `GetHoldingsOverview`

## Command Wiring

**File**: `cmd/ibctl/internal/ibctlcmd/ibctlcmd.go`

- `NewDownloader` passes account mappings from config

## README

- Update config format to include `accounts` section
- Update Flex Query setup to include Transfers, Trade Transfers, Corporate Actions sections
- Update data storage table for per-account directory structure
- Note that old flat data requires re-download after adding accounts

## Implementation Sequence

1. Proto changes (add account_id, new messages) + `make generate`
2. Config changes (accounts section)
3. XML parsing (multi-account, new sections)
4. Download (per-account storage, new conversion functions)
5. Merge (account-aware)
6. Tax lots (transfers, corporate actions, per-account FIFO)
7. Holdings (combined default, --account flag)
8. Command wiring + README

## Verification

1. `make all` passes (lint + test)
2. User updates Flex Query in IBKR portal (add Transfers, Trade Transfers, Corporate Actions sections)
3. User adds `accounts` section to `ibctl.yaml`
4. `ibctl download` — shows per-account counts, creates `v1/<alias>/` directories
5. `ibctl holdings overview` — combined view with correct quantities matching IBKR positions
6. `ibctl holdings overview --account rrsp` — single-account view
7. Position discrepancy warnings should be minimal (transfers fill in missing buy lots)
