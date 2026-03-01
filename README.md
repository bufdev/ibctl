# ibctl

## TODO:

- other manual things other than just cash adjustments, and a list of them

A CLI tool for analyzing Interactive Brokers (IBKR) holdings and trades. Downloads data via the IBKR Flex Query API, computes FIFO tax lots, and displays holdings with prices, positions, and USD conversions. Supports multiple IBKR accounts.

## Prerequisites

- An [Interactive Brokers](https://www.interactivebrokers.com) account
- Go 1.25+

## Quick Start

```bash
# Create an ibctl directory.
mkdir ~/ibkr && cd ~/ibkr

# Initialize configuration.
ibctl config init

# Edit the config to add your Flex Query ID and account aliases.
ibctl config edit

# Set the IBKR token.
export IBKR_FLEX_WEB_SERVICE_TOKEN="your-flex-web-service-token"

# View holdings (downloads data automatically).
ibctl holding list
```

## Directory Structure

All commands operate on an **ibctl directory** specified by `--dir` (defaults to the current directory). This directory has a well-known layout:

```
<dir>/
├── ibctl.yaml                          # Configuration file
├── data/                               # Persistent — do not delete
│   └── accounts/<alias>/
│       └── trades.json                 # Incrementally merged trade history
├── cache/                              # Safe to delete — re-populated on next download
│   ├── accounts/<alias>/
│   │   ├── positions.json              # Latest IBKR-reported positions snapshot
│   │   ├── transfers.json              # Position transfers (ACATS, FOP, internal)
│   │   ├── trade_transfers.json        # Cost basis for transferred positions
│   │   ├── corporate_actions.json      # Stock splits, mergers, spinoffs
│   │   └── cash_positions.json         # Cash balances by currency
│   └── fx/<BASE>.<QUOTE>/
│       └── rates.json                  # Daily FX rates per currency pair
├── activity_statements/                # User-managed IBKR Activity Statement CSVs
│   └── <alias>/*.csv
└── seed/                               # Optional — pre-transfer tax lots from previous brokers
    └── <alias>/transactions.json
```

- **`data/`** contains `trades.json` per account, incrementally merged across downloads. This is the only directory that accumulates over time — IBKR limits each download to 365 days, so older trades can't be re-downloaded.
- **`cache/`** contains everything else: position snapshots, transfers, FX rates. Safe to delete entirely — the next `ibctl download` re-populates it.
- **`activity_statements/`** contains Activity Statement CSVs you download from the IBKR portal. ibctl reads them at command time and never modifies them.
- **`seed/`** (optional) contains permanent transaction history imported from previous brokers (e.g., UBS, RBC).

## IBKR Flex Query Setup

Follow these steps in the IBKR portal to create a Flex Query and generate an API token.

### Create a Flex Query

1. Log in to [IBKR Account Management](https://www.interactivebrokers.com/portal).
2. Navigate to **Performance & Reports** > **Flex Queries**.
3. In the **Activity Flex Query** section, click the **+** button.
4. Set the **Query Name** to something descriptive (e.g., "ibctl").
5. Under **Sections**, add the following sections, selecting all fields for each:
   - **Trades**
   - **Open Positions**
   - **Cash Transactions** (used for FX rate extraction)
   - **Cash Report** (provides cash balances by currency)
   - **Transfers (ACATS, Internal)** (captures positions transferred from other brokers)
   - **Incoming/Outgoing Trade Transfers** (preserves cost basis and holding period)
   - **Corporate Actions** (captures stock splits, mergers, spinoffs)
6. Under **Delivery Configuration**, set:
   - **Format**: `XML`
   - **Period**: `Last 365 Calendar Days`
7. Under **General Configuration**, set:
   - **Date Format**: `yyyyMMdd`
   - **Time Format**: `HHmmss`
   - **Date/Time Separator**: `; (semi-colon)`
   - **Include Canceled Trades?**: `No`
   - **Include Currency Rates?**: `Yes`
   - **Include Audit Trail Fields?**: `No`
   - **Breakout by Day?**: `No`
8. Click **Save**.
9. Note the **Query ID** displayed next to the query name.

**Note on trade history**: IBKR limits Flex Query periods to 365 calendar days. To capture older trades, change the Period in the IBKR portal to cover a different date range and run `ibctl download` again — new trades are merged into the existing cache, deduplicated by trade ID.

### Generate a Flex Web Service Token

1. On the same **Flex Queries** page, find the **Flex Web Service Configuration** section.
2. Click the **gear icon** to configure.
3. Generate a token and copy it securely.
4. Set it as the `IBKR_FLEX_WEB_SERVICE_TOKEN` environment variable.

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `IBKR_FLEX_WEB_SERVICE_TOKEN` | Yes (for `download`) | IBKR Flex Web Service token. Read-only — can only retrieve reports, not make trades. Never store in config files or version control. |

## Configuration

The `ibctl.yaml` file lives in the ibctl directory:

```yaml
version: v1
flex_query_id: "123456"
accounts:
  rrsp: "U1234567"
  holdco: "U2345678"
  individual: "U3456789"
symbols:
  - name: AAPL
    category: EQUITY
    type: STOCK
    sector: TECH
    geo: US
```

- `flex_query_id` — your IBKR Flex Query ID (required)
- `accounts` — maps user-chosen aliases to IBKR account IDs (required). Account numbers are confidential — only aliases appear in output and directory names.
- `symbols` — optional classification metadata for holdings display (category, type, sector, geo)

## Usage

```bash
# Set the IBKR token.
export IBKR_FLEX_WEB_SERVICE_TOKEN="your-flex-web-service-token"

# View combined holding list (downloads data automatically).
ibctl holding list
ibctl holding list --format csv
ibctl holding list --format json
ibctl holding list --cached    # Skip download, use cached data only

# Force re-download of IBKR data (all accounts).
ibctl download

# Probe the API to see what data is available per account.
ibctl probe

# Archive the ibctl directory to a zip file.
ibctl data zip -o backup.zip

# Use a different ibctl directory (default is current directory).
ibctl holding list --dir ~/Documents/ibkr
```

## Commands

| Command | Description |
|---------|-------------|
| `ibctl config init` | Create a new ibctl.yaml in the ibctl directory |
| `ibctl config edit` | Edit ibctl.yaml in `$EDITOR` |
| `ibctl config validate` | Validate ibctl.yaml |
| `ibctl data zip -o <file>` | Archive the ibctl directory to a zip file |
| `ibctl download` | Download and cache IBKR data via Flex Query API |
| `ibctl holding list` | Display holdings with prices, positions, and classifications |
| `ibctl probe` | Probe the API and show per-account data counts |

All commands accept `--dir` to specify the ibctl directory (defaults to `.`).

## Seeding Historical Data

IBKR limits all data access to 365 days per request. To get your full trade history, download Activity Statement CSVs from the IBKR portal.

### Setup

1. Create subdirectories for each account using your aliases:
   ```bash
   mkdir -p activity_statements/rrsp
   mkdir -p activity_statements/holdco
   mkdir -p activity_statements/individual
   ```

2. For each account, log in to [IBKR Account Management](https://www.interactivebrokers.com/portal).

3. Go to **Performance & Reports** > **Statements**.

4. Select **Activity Statement**, **Custom Date Range**, and click **Download CSV**.

5. Download yearly chunks (365-day maximum per file).

6. Save each CSV in the account's subdirectory. Filenames don't matter — ibctl reads all `*.csv` files recursively.

7. Run `ibctl holding list` — data from the CSVs is merged with Flex Query API data.

### How Merging Works

At command time, ibctl merges three data sources per account. CSV data takes precedence for overlapping date ranges:

1. **Activity Statement CSVs** (`activity_statements/<alias>/*.csv`) — primary source of truth for trades
2. **Seed data** (`seed/<alias>/transactions.json`) — imported transactions from previous brokers
3. **Flex Query cache** (`data/accounts/<alias>/trades.json`) — recent trades from the API, used only for dates not covered by CSVs

## Implementation

### Data Files

All data files use newline-separated protobuf JSON (one message per line). Monetary values use `standard.money.v1.Money` (units + micros for 6 decimal places). Dates use `standard.time.v1.Date` (year, month, day).

| File | Proto Message | Merge Strategy | Purpose |
|------|--------------|----------------|---------|
| `trades.json` | `ibctl.data.v1.Trade` | Deduplicated by trade ID | Persistent trade history. Incrementally merged across downloads so the cache grows over time. |
| `positions.json` | `ibctl.data.v1.Position` | Overwritten each download | IBKR-reported positions snapshot. Provides current market prices and verification data. **Not the source of truth** for quantities or cost basis — those are computed via FIFO from trades. |
| `transfers.json` | `ibctl.data.v1.Transfer` | Overwritten each download | Position transfers (ACATS, ATON, FOP, internal). Transfer-ins with a non-zero price become synthetic buy trades for FIFO. |
| `trade_transfers.json` | `ibctl.data.v1.TradeTransfer` | Overwritten each download | Preserves **original trade date** and **cost basis** for transferred positions (long-term vs short-term capital gains). |
| `corporate_actions.json` | `ibctl.data.v1.CorporateAction` | Overwritten each download | Stock splits, mergers, spinoffs for audit purposes. |
| `cash_positions.json` | `ibctl.data.v1.CashPosition` | Overwritten each download | Cash balances by currency from the IBKR Cash Report section. |
| `rates.json` | `ibctl.data.v1.ExchangeRate` | Deduplicated by date | Per-pair FX rates from [Bank of Canada](https://www.bankofcanada.ca) (X→CAD) and [frankfurter.dev](https://frankfurter.dev) (X→USD). Only missing dates are fetched. |

### Seed Data

The optional `seed/` directory contains permanent, manually curated transaction history from previous brokers. `transactions.json` uses the `ibctl.data.v1.ImportedTransaction` proto covering all transaction types (buys, sells, splits, dividends, interest, fees, etc.). Only security-affecting transactions are converted to Trade protos for FIFO processing.

### Data Pipeline

The `holding list` command runs:

1. **Download**: Fetches all accounts' data from the IBKR Flex Query API. Trades are incrementally merged. FX rates are eagerly downloaded for all currency pairs from the earliest trade date to today.
2. **Merge**: Combines Activity Statement CSVs + seed data + Flex Query cache, with CSV data taking precedence for overlapping dates.
3. **FIFO**: Computes tax lots grouped by (account, symbol). Transfers and trade transfers are converted to synthetic trades. Buys before sells within the same date.
4. **Aggregation**: Tax lots are aggregated into positions with weighted average cost basis, then combined across accounts.
5. **Verification**: Computed positions are compared against IBKR-reported positions. Cost basis discrepancies > 0.1% are logged as warnings.
6. **Display**: Holdings are rendered with USD conversions (via FX rates), market value, unrealized P&L split into short-term and long-term capital gains, and optional symbol classifications.
