# Activity Statements as Seed Data + Flex Query Supplements

## Context

IBKR limits all data access to 365-day windows. The user has 3 accounts with history going back to April 2021 (~5 years). Rather than importing CSVs into ibctl's cache as a one-time operation, the Activity Statement CSVs become a **persistent, user-managed seed data directory** that ibctl reads directly at command time. The Flex Query API supplements with the latest data beyond what's in the CSVs.

Config points at the directory: `activity_statements_dir: ~/Documents/ibkr-statements`. User organizes it by account subdirectories. ibctl reads all `*.csv` files across all subdirectories, deduplicates, and merges with Flex Query data at command time.

## Config Change

Add `activity_statements_dir` to config:

```yaml
version: v1
query_id: "1419229"
# Directory containing IBKR Activity Statement CSVs, organized by account subdirectory.
activity_statements_dir: ~/Documents/ibkr-statements
```

The directory structure:
```
~/Documents/ibkr-statements/
├── account-1/
│   ├── 2021-04-01_2022-03-31.csv
│   ├── 2022-04-01_2023-03-31.csv
│   ├── 2023-04-01_2024-03-31.csv
│   └── 2024-04-01_2025-02-28.csv
├── account-2/
│   └── ...
└── account-3/
    └── ...
```

Filenames don't matter — ibctl reads all `*.csv` recursively.

## CSV Sections to Parse

From the sample CSV, parse these sections (skip Account Information entirely — contains identifying info):

| Section | Row filter | Key fields | Use |
|---------|-----------|------------|-----|
| **Trades** (Stocks) | `Trades,Data,Order` where Asset Category = `Stocks` | Symbol, Date/Time, Quantity, T. Price, Currency, Proceeds, Comm/Fee, Realized P/L, Code | Trade history, FIFO tax lots |
| **Trades** (Forex) | `Trades,Data,Order` where Asset Category = `Forex` | Symbol, Date/Time, Quantity, T. Price, Currency | FX conversions |
| **Open Positions** | `Open Positions,Data,Summary` | Symbol, Currency, Quantity, Cost Price, Cost Basis, Close Price, Value, Unrealized P/L | Current positions snapshot |
| **Dividends** | `Dividends,Data` (not Total) | Currency, Date, Description, Amount | Dividend income |
| **Withholding Tax** | `Withholding Tax,Data` (not Total) | Currency, Date, Description, Amount, Code | Tax withheld on dividends |
| **Interest** | `Interest,Data` (not Total) | Currency, Date, Description, Amount | Interest income/expense |
| **Financial Instrument Information** (Stocks) | `Financial Instrument Information,Data,Stocks` | Symbol, Description, Conid, Security ID, Listing Exch, Type (COMMON/ETF/ADR) | Symbol metadata — replaces manual config classification |
| **Financial Instrument Information** (Bonds) | Second header variant with Issuer, Maturity | Symbol, Description, Conid, Security ID, Issuer, Maturity, Type (Corp) | Bond metadata |

**Sections to skip:** Statement, Account Information, Net Asset Value, Change in NAV, Mark-to-Market Performance Summary, Realized & Unrealized Performance Summary, Cash Report, Forex Balances, Transaction Fees, Fees, Interest Accruals, Change in Dividend Accruals, Codes, Notes/Legal Notes.

## CSV Parsing Notes

- Multi-section format: first column is section name, second is row type (`Header`, `Data`, `SubTotal`, `Total`)
- Can't use standard `csv.Reader` naively — varying column counts per section
- Quantities may have commas in quotes (e.g., `"-2,290"`)
- Negative quantity = sell, positive = buy
- Date/Time format: `"2026-01-02, 09:30:00"`
- No trade ID — generate deterministic key from `Symbol + DateTime + Quantity + Price`
- Forex trades header differs from stock trades header (different columns)
- Financial Instrument Information has two header variants (Stocks vs Bonds)

## Architecture

```
Command (e.g., holdings overview)
  │
  ├─ Read Activity Statement CSVs (seed data, user-managed)
  │   └─ ibkractivitycsv.ParseDirectory(dir) → parsed sections
  │
  ├─ Read Flex Query cache (supplement, ibctl-managed)
  │   └─ protoio.ReadMessagesJSON(trades.json) → cached trades
  │
  ├─ Merge: CSV trades + cached trades, dedup by composite key
  │
  └─ Compute: tax lots, positions, holdings overview
```

The merge happens at **read time**, not write time. The CSVs are never modified. The Flex Query cache is the only thing ibctl writes to.

## New Packages

### `internal/pkg/ibkractivitycsv/ibkractivitycsv.go`

Provider-specific CSV parser. Parses IBKR Activity Statement CSV files.

**Exported types** (plain Go structs, not protos — this is a parsing package):
- `ActivityStatement` — all parsed sections from one file
- `Trade` — Symbol, DateTime, AssetCategory, CurrencyCode, Quantity, TradePrice, Proceeds, Commission, RealizedPL, Code
- `ForexTrade` — Symbol, DateTime, CurrencyCode, Quantity, TradePrice, Proceeds, Commission
- `Position` — Symbol, AssetCategory, CurrencyCode, Quantity, CostPrice, CostBasis, ClosePrice, Value, UnrealizedPL
- `Dividend` — CurrencyCode, Date, Description, Amount
- `WithholdingTax` — CurrencyCode, Date, Description, Amount, Code
- `Interest` — CurrencyCode, Date, Description, Amount
- `InstrumentInfo` — Symbol, Description, Conid, SecurityID, ListingExchange, InstrumentType, AssetCategory, Issuer, Maturity

**Exported functions:**
- `ParseFile(filePath string) (*ActivityStatement, error)`
- `ParseDirectory(dirPath string) ([]*ActivityStatement, error)` — reads all `*.csv` recursively

### `internal/ibctl/ibctlmerge/ibctlmerge.go`

Merges data from Activity Statement CSVs and Flex Query cache into a unified view. Used by commands at read time.

**Exported functions:**
- `MergedData` struct containing all merged trades, positions, dividends, etc.
- `Merge(csvStatements []*ibkractivitycsv.ActivityStatement, flexCachePath string) (*MergedData, error)`

This converts CSV structs → protos, merges with cached protos, deduplicates.

## Changes to Existing Code

### `internal/ibctl/ibctlconfig/ibctlconfig.go`

- Add `ActivityStatementsDir string` to `ExternalConfig` (`yaml:"activity_statements_dir"`)
- Add `ActivityStatementsDirPath string` to `Config` (resolved via `xos.ExpandHome`)
- Not required — if empty, only Flex Query data is used

### `cmd/ibctl/main.go`

- No new command needed — the CSVs are read implicitly by existing commands

### `cmd/ibctl/internal/command/holdings/holdingsoverview/holdingsoverview.go`

- Read Activity Statement CSVs via `ibkractivitycsv.ParseDirectory(config.ActivityStatementsDirPath)`
- Merge with Flex Query cache via `ibctlmerge`
- Compute holdings from merged data

### `README.md`

Add "Seeding Historical Data" section with exact instructions:

1. Create a directory for Activity Statements (e.g., `~/Documents/ibkr-statements`)
2. Create a subdirectory for each IBKR account (e.g., `my-account/`)
3. For each account, log in to IBKR Account Management
4. Go to Performance & Reports > Statements
5. Select Activity Statement, Custom Date Range, CSV format
6. Download yearly chunks (365-day max per file):
   - 2021-04-01 to 2022-03-31
   - 2022-04-01 to 2023-03-31
   - 2023-04-01 to 2024-03-31
   - 2024-04-01 to 2025-02-28
7. Save each CSV in the account's subdirectory
8. Add `activity_statements_dir: ~/Documents/ibkr-statements` to config
9. Run `ibctl holdings overview` — data from CSVs + Flex Query API are merged

## File Manifest

**Create:**
- `internal/pkg/ibkractivitycsv/ibkractivitycsv.go` — Activity Statement CSV parser
- `internal/ibctl/ibctlmerge/ibctlmerge.go` — Merge CSV + Flex Query data

**Modify:**
- `internal/ibctl/ibctlconfig/ibctlconfig.go` — Add `activity_statements_dir` field
- `cmd/ibctl/internal/command/holdings/holdingsoverview/holdingsoverview.go` — Read and merge CSVs
- `README.md` — Seeding instructions

**Unchanged:**
- `internal/ibctl/ibctldownload/` — Flex Query download stays as-is
- `internal/pkg/ibkrflexquery/` — Flex Query API client stays as-is
- `cmd/ibctl/internal/command/download/` — Download command stays as-is

## Verification

1. `make generate && make all` — 0 issues
2. Place sample.csv in `~/Documents/ibkr-statements/test-account/`
3. Add `activity_statements_dir: ~/Documents/ibkr-statements` to config
4. `ibctl holdings overview` — shows data from the CSV
5. `ibctl download` — downloads latest via API
6. `ibctl holdings overview` — shows merged CSV + API data
