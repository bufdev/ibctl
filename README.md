# ibctl

A CLI tool for analyzing Interactive Brokers (IBKR) holdings and trades. Downloads data via the IBKR Flex Query API, computes FIFO tax lots, and displays holdings with average prices and positions. Supports multiple IBKR accounts with per-account data storage.

## Prerequisites

- An [Interactive Brokers](https://www.interactivebrokers.com) account
- Go 1.25+

## IBKR Flex Query Setup

Follow these exact steps in the IBKR portal to create a Flex Query and generate an API token.

### Create a Flex Query

1. Log in to [IBKR Account Management](https://www.interactivebrokers.com/portal).
2. Navigate to **Performance & Reports** > **Flex Queries**.
3. In the **Activity Flex Query** section, click the **+** button in the top right corner of the panel.
4. Set the **Query Name** to something descriptive (e.g., "ibctl").
5. Under **Sections**, add the following sections, selecting all fields for each:
   - **Trades**
   - **Open Positions**
   - **Cash Transactions** (used for FX rate extraction)
   - **Transfers (ACATS, Internal)** (captures positions transferred from other brokers)
   - **Incoming/Outgoing Trade Transfers** (preserves cost basis and holding period for transferred positions)
   - **Corporate Actions** (captures stock splits, mergers, spinoffs)
6. Under **Delivery Configuration**, set:
   - **Format**: `XML`
   - **Period**: `Last 365 Calendar Days` (this is the maximum; see note below about older history)
7. Under **General Configuration**, set:
   - **Date Format**: `yyyyMMdd`
   - **Time Format**: `HHmmss`
   - **Date/Time Separator**: `; (semi-colon)`
   - **Include Canceled Trades?**: `No`
   - **Include Currency Rates?**: `Yes`
   - **Include Audit Trail Fields?**: `No`
   - **Breakout by Day?**: `No`
8. Click **Save**.
9. Note the **Query ID** displayed next to the query name in the list. You will need this for the configuration file.

**Note on trade history**: IBKR limits Flex Query periods to 365 calendar days. To capture older trades, change the Period in the IBKR portal to cover a different date range and run `ibctl download` again — new trades will be merged into the existing cache, deduplicated by trade ID.

### Generate a Flex Web Service Token

1. On the same **Flex Queries** page, find the **Flex Web Service Configuration** section on the right side.
2. Click the **gear icon** to configure.
3. Generate a token and copy it securely. This token is used as the `IBKR_FLEX_WEB_SERVICE_TOKEN` environment variable.
4. The token is valid for the duration shown. Regenerate it when it expires.

## File Locations

| Path | Purpose | Override |
|------|---------|----------|
| `ibctl.yaml` | Configuration file in current directory | `--config` flag |
| `<data_dir>/v1/accounts/<alias>/` | Per-account downloaded data cache | Set `data_dir` and `accounts` in config |
| `<data_dir>/v1/fx/` | FX rate data per currency pair | Derived from `data_dir` |

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `IBKR_FLEX_WEB_SERVICE_TOKEN` | Yes (for `download`) | Your IBKR Flex Web Service token. This is read-only — it can only retrieve reports, not make trades or modify your account. Never store this in configuration files or commit it to version control. |

## Configuration

### Initialize Configuration

```bash
ibctl config init
```

This creates a new `ibctl.yaml` in the current directory. Edit it to fill in your Flex Query ID, account mappings, and optional symbol classifications.

### Edit Configuration

```bash
ibctl config edit
```

Opens the configuration file in `$EDITOR`. Creates the file with a documented template if it does not exist.

### Configuration Format

```yaml
# The configuration file version (required, must be v1).
version: v1
# The data directory for ibctl to store downloaded and computed data (required).
data_dir: ~/Documents/ibctl
# The Flex Query ID (required, visible next to your query in IBKR portal).
flex_query_id: "123456"
# Account aliases mapping (required).
# Maps user-chosen aliases to IBKR account IDs.
# Account numbers are confidential — aliases are used in output and directory names.
# Aliases must be lowercase alphanumeric with hyphens.
accounts:
  rrsp: "U1234567"
  holdco: "U2345678"
  individual: "U3456789"
# Directory containing IBKR Activity Statement CSVs (required).
# Organize by account subdirectory. See "Seeding Historical Data" below.
activity_statements_dir: ~/Documents/ibkr-statements
# Optional symbol classifications for holdings output.
symbols:
  - name: AAPL
    category: EQUITY
    type: STOCK
    sector: TECH
  - name: VTI
    category: EQUITY
    type: ETF
    sector: BROAD
```

### Validate Configuration

```bash
ibctl config validate
```

## Usage

```bash
# Set the IBKR token.
export IBKR_FLEX_WEB_SERVICE_TOKEN="your-flex-web-service-token"

# View combined holdings overview (downloads data automatically if not cached).
ibctl holdings overview
ibctl holdings overview --format csv
ibctl holdings overview --format json

# Force re-download of IBKR data (all accounts).
ibctl download

# Probe the API to see what data is available per account.
ibctl probe
```

Data is downloaded automatically when commands need it. Use `ibctl download` to force a refresh. Each download merges new data with the existing cache — trades are deduplicated by trade ID, so it is safe to run repeatedly. Data is stored per account under `<data_dir>/v1/accounts/<alias>/`.

## Commands

| Command | Description |
|---------|-------------|
| `ibctl config init` | Create a new configuration file and print its path |
| `ibctl config edit` | Edit the configuration file in `$EDITOR` |
| `ibctl config validate` | Validate the configuration file |
| `ibctl download` | Force re-download and cache IBKR data via Flex Query API |
| `ibctl holdings overview` | Display combined holdings with prices, positions, and classifications |
| `ibctl probe` | Probe the API and show per-account data counts |

## Seeding Historical Data

IBKR limits all data access (API and portal) to 365 days per request. To get your full trade history, download Activity Statement CSVs from the IBKR portal and point ibctl at them. ibctl reads these files directly at command time and merges them with data from the Flex Query API.

### Setup

1. Create a directory for your Activity Statements:
   ```bash
   mkdir -p ~/Documents/ibkr-statements
   ```

2. Create a subdirectory for each IBKR account using your aliases:
   ```bash
   mkdir ~/Documents/ibkr-statements/rrsp
   mkdir ~/Documents/ibkr-statements/holdco
   mkdir ~/Documents/ibkr-statements/individual
   ```

3. For each account, log in to [IBKR Account Management](https://www.interactivebrokers.com/portal).

4. Go to **Performance & Reports** > **Statements**.

5. Select **Activity Statement**, **Custom Date Range**, and click **Download CSV**.

6. Download yearly chunks (365-day maximum per file). For an account opened April 2021:
   - 2021-04-01 to 2022-03-31
   - 2022-04-01 to 2023-03-31
   - 2023-04-01 to 2024-03-31
   - 2024-04-01 to 2025-03-31
   - 2025-03-01 to 2026-02-28

7. Save each CSV in the account's subdirectory. Filenames don't matter — ibctl reads all `*.csv` files recursively.

8. Add the directory to your config:
   ```bash
   ibctl config edit
   ```
   Set `activity_statements_dir: ~/Documents/ibkr-statements`.

9. Run `ibctl holdings overview` — data from the CSVs is merged with any Flex Query API data.

### How It Works

The Activity Statement CSVs are **seed data** that you manage. ibctl never modifies them. At command time, ibctl:

1. Reads all CSVs from the configured directory (trades, positions, dividends, interest, instrument info)
2. Reads any cached Flex Query data per account (from `ibctl download`)
3. Merges and deduplicates — CSV data takes precedence for overlapping trades
4. Converts transfers (ACATS) to synthetic trades for FIFO processing
5. Computes tax lots, positions, and holdings from the merged data

To keep data current, the Flex Query API provides the latest 365 days. To add older history, download more CSVs.

## Multi-Account Support

ibctl supports multiple IBKR accounts via the `accounts` section in the config. Each account is identified by an alias (e.g., "rrsp", "holdco") that maps to an IBKR account ID.

- **Downloaded data** is stored per account under `<data_dir>/v1/accounts/<alias>/`
- **Holdings overview** shows a combined view aggregated across all accounts
- **Transfers** between accounts and from other brokers (ACATS) are tracked and converted to synthetic trades for accurate FIFO computation
- **Corporate actions** (stock splits, mergers, spinoffs) are captured from the Flex Query API

Account numbers are confidential — only aliases appear in output and directory names.

## Implementation

### Data Directory Structure

All cached data lives under `<data_dir>/v1/`. Per-account data is stored in `accounts/<alias>/`, and FX rate data is stored in `fx/`.

```
<data_dir>/v1/
├── accounts/                          # Per-account cached data from IBKR Flex Query API.
│   ├── rrsp/
│   │   ├── trades.json                # All trades (buys/sells) for this account.
│   │   ├── positions.json             # Latest IBKR-reported open positions snapshot.
│   │   ├── transfers.json             # Position transfers (ACATS, FOP, internal).
│   │   ├── trade_transfers.json       # Cost basis metadata for transferred positions.
│   │   └── corporate_actions.json     # Stock splits, mergers, spinoffs.
│   ├── holdco/
│   │   └── (same files)
│   └── individual/
│       └── (same files)
└── fx/                                # FX rate data per currency pair.
    └── exchange_rates.json            # Currency exchange rates (all pairs).
```

### Data Files

All files use newline-separated protobuf JSON (one message per line), serialized with `protojson` using proto field names. Monetary values use `standard.money.v1.Money` (units + micros for 6 decimal places). Dates use `standard.time.v1.Date` (year, month, day).

| File | Proto Message | Merge Strategy | Purpose |
|------|--------------|----------------|---------|
| `trades.json` | `ibctl.data.v1.Trade` | Deduplicated by trade ID, new overwrites old | All trades (buys/sells) for this account. Incrementally merged across downloads so the cache grows over time, overcoming IBKR's 365-day API limit. |
| `positions.json` | `ibctl.data.v1.Position` | Overwritten entirely on each download | Latest snapshot of IBKR-reported open positions. Provides current market prices (LAST PRICE column) and serves as verification data — computed holdings are compared against these to detect discrepancies. **Not the source of truth for quantities or cost basis** — those are computed via FIFO from trades. |
| `transfers.json` | `ibctl.data.v1.Transfer` | Overwritten on each download | Position transfers between accounts or brokers (ACATS, ATON, FOP, internal). Transfer-in records with a non-zero transfer price are converted to synthetic buy trades for FIFO processing. Transfer-out records become synthetic sell trades. Transfers without a price are informational only. |
| `trade_transfers.json` | `ibctl.data.v1.TradeTransfer` | Overwritten on each download | Cost basis and holding period metadata for positions transferred from other brokers. Preserves the **original trade date** (for long-term vs short-term capital gains) and **original cost basis** from the source broker. Converted to synthetic buy trades using the original date, not the transfer date. |
| `corporate_actions.json` | `ibctl.data.v1.CorporateAction` | Overwritten on each download | Corporate action events (forward/reverse splits, mergers, spinoffs) for audit purposes. |
| `exchange_rates.json` | `ibctl.data.v1.ExchangeRate` | Deduplicated by date + currency pair | FX rates from IBKR cash transactions and [frankfurter.dev](https://frankfurter.dev). Each rate has a date, base/quote currency codes, rate value, and provider field. |

### Seed Data

The optional `seed_dir` in the config points to a directory of permanent, manually curated data from previous brokers (e.g., UBS, RBC). This data is never modified by ibctl.

```
<seed_dir>/
└── <alias>/
    └── transactions.json              # Normalized transaction history from previous brokers.
```

`transactions.json` uses the `ibctl.data.v1.ImportedTransaction` proto, which represents all transaction types from the previous broker: buys, sells, splits, stock dividends, expiries, redemptions, dividends, interest, fees, withholding tax, transfers, deposits, and withdrawals. At read time, only security-affecting transactions (buys, sells, splits, expiries, redemptions, stock dividends) are converted to Trade protos for FIFO processing. The rest are stored for income tracking and audit.

### Data Pipeline

The `holdings overview` command runs the following pipeline:

1. **Download** (`ibctl download` or automatic): Fetches all accounts' data from the IBKR Flex Query API in a single API call. Trades are incrementally merged with the cache (deduplicated by trade ID). Positions are overwritten with the latest snapshot. FX rates are extracted from cash transactions and supplemented by [frankfurter.dev](https://frankfurter.dev) for any missing dates.

2. **Merge** (`ibctlmerge`): Combines three data sources per account, with CSV data taking precedence for overlapping date ranges:
   - **Activity Statement CSVs** (`activity_statements_dir/<alias>/*.csv`) — primary source of truth for trades. These cover dates that the CSVs span; Flex Query trades within this range are excluded because the two sources represent the same trades at different granularities (CSVs consolidate order executions, Flex Query splits them).
   - **Seed data** (`seed_dir/<alias>/transactions.json`) — imported transactions from previous brokers, converted to Trade protos for FIFO.
   - **Flex Query cache** (`accounts/<alias>/trades.json`) — recent trades from the API, used only for dates not covered by CSVs.

3. **FIFO Tax Lot Computation** (`ibctltaxlot`): Processes all merged trades using First-In-First-Out ordering, grouped by (account, symbol). Transfers and trade transfers are converted to synthetic trades before processing. Buys create new lots, sells consume the oldest lots first. Short positions are supported (sell-to-open creates negative lots, buy-to-close closes them). Within the same date, buys are processed before sells to handle same-day buy+sell scenarios.

4. **Position Aggregation**: Tax lots are aggregated into per-account positions with weighted average cost basis, then combined across accounts for display.

5. **Verification**: Computed positions are compared against IBKR-reported positions (`positions.json`). Quantity mismatches and cost basis discrepancies exceeding 0.1% are logged as warnings. Positions that exist only in computed data or only in IBKR's report are also flagged.

6. **Display**: Holdings are rendered with current market prices from IBKR positions and optional symbol classifications from the config.
