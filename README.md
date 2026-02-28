# ibctl

A CLI tool for analyzing Interactive Brokers (IBKR) holdings and trades. Downloads data via the IBKR Flex Query API, computes FIFO tax lots, and displays holdings with average prices and positions.

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
5. Under **Sections**, add the following three sections, selecting all fields for each:
   - **Trades**
   - **Open Positions**
   - **Cash Transactions** (used for FX rate extraction)
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
| `<data_dir>/v1/` | Downloaded data cache | Set `data_dir` in config |

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `IBKR_FLEX_WEB_SERVICE_TOKEN` | Yes (for `download`) | Your IBKR Flex Web Service token. This is read-only — it can only retrieve reports, not make trades or modify your account. Never store this in configuration files or commit it to version control. |

## Configuration

### Initialize Configuration

```bash
ibctl config init
```

This creates a new `config.yaml` in the configuration directory and prints the file path. Edit it to fill in your Flex Query ID and optional symbol classifications.

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

# View holdings overview (downloads data automatically if not cached).
ibctl holdings overview
ibctl holdings overview --format csv
ibctl holdings overview --format json

# Force re-download of IBKR data.
ibctl download
```

Data is downloaded automatically when commands need it. Use `ibctl download` to force a refresh. Each download merges new data with the existing cache — trades are deduplicated by trade ID, so it is safe to run repeatedly.

## Commands

| Command | Description |
|---------|-------------|
| `ibctl config init` | Create a new configuration file and print its path |
| `ibctl config edit` | Edit the configuration file in `$EDITOR` |
| `ibctl config validate` | Validate the configuration file |
| `ibctl download` | Force re-download and cache IBKR data via Flex Query API |
| `ibctl holdings overview` | Display holdings with prices, positions, and classifications |

## Seeding Historical Data

IBKR limits all data access (API and portal) to 365 days per request. To get your full trade history, download Activity Statement CSVs from the IBKR portal and point ibctl at them. ibctl reads these files directly at command time and merges them with data from the Flex Query API.

### Setup

1. Create a directory for your Activity Statements:
   ```bash
   mkdir -p ~/Documents/ibkr-statements
   ```

2. Create a subdirectory for each IBKR account:
   ```bash
   mkdir ~/Documents/ibkr-statements/RRSP
   mkdir ~/Documents/ibkr-statements/HoldCo
   mkdir ~/Documents/ibkr-statements/Individual
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
2. Reads any cached Flex Query data (from `ibctl download`)
3. Merges and deduplicates — Flex Query data takes precedence for overlapping trades
4. Computes tax lots, positions, and holdings from the merged data

To keep data current, the Flex Query API provides the latest 365 days. To add older history, download more CSVs.

## Data Storage

All data is cached as protobuf-JSON files under the data directory (`<data_dir>/v1/` as configured in `ibctl.yaml`). Each file stores newline-separated proto JSON (one message per line), serialized using `protojson` with proto field names. `metadata.json` is a single message.

| File | Protobuf Message | Description |
|------|-----------------|-------------|
| `trades.json` | `ibctl.data.v1.Trade` | All trades from the IBKR Flex Query. Each trade includes trade ID, dates, symbol, side (buy/sell), quantity, price, proceeds, commission, currency code, and FIFO realized P&L. |
| `positions.json` | `ibctl.data.v1.Position` | Open positions as reported by IBKR, including quantity, cost basis price, market price, market value, currency code, and unrealized P&L. |
| `tax_lots.json` | `ibctl.data.v1.TaxLot` | FIFO tax lots computed from trades. Each lot tracks symbol, open date, remaining quantity, cost basis price, and currency code. Long-term status (held >= 1 year) is computed dynamically at display time. |
| `exchange_rates.json` | `ibctl.data.v1.ExchangeRate` | Currency exchange rates with date, base/quote currency codes, rate (units + micros), and provider (ibkr or [frankfurter.dev](https://frankfurter.dev)). |
| `metadata.json` | `ibctl.data.v1.Metadata` | Download timestamp, whether computed positions matched IBKR-reported positions, and any verification discrepancy notes. |

Monetary values use `standard.money.v1.Money` with units and micros (6 decimal places). Dates use `standard.time.v1.Date` with year, month, and day fields. Timestamps use `google.protobuf.Timestamp`.
