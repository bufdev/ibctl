# ibctl

A CLI tool for analyzing Interactive Brokers (IBKR) holdings and trades. Downloads data via the IBKR Flex Query API, computes FIFO tax lots, and displays holdings with average prices and positions.

## Prerequisites

- An [Interactive Brokers](https://www.interactivebrokers.com) account
- Go 1.25+

## IBKR Flex Query Setup

Follow these exact steps in the IBKR portal to create a Flex Query and generate an API token.

### Create a Flex Query

1. Log in to [Interactive Brokers Client Portal](https://www.interactivebrokers.com/portal).
2. Navigate to **Performance & Reports** in the top menu.
3. Click **Flex Queries** in the left sidebar.
4. Under **Custom Flex Queries**, click the **+** (Create) button.
5. Set the **Query Name** to something descriptive (e.g., "ibctl").
6. Set **Format** to **XML**.
7. Under **Sections**, add the following three sections:
   - **Trades**: Click **Trades**, then select all fields. Set the date range to cover your full trade history (e.g., from account opening).
   - **Open Positions**: Click **Open Positions**, then select all fields.
   - **Cash Transactions**: Click **Cash Transactions**, then select all fields. This is used for FX rate extraction.
8. Click **Save** to save the query.
9. Note the **Query ID** displayed next to the query name in the list. You will need this for the configuration file.

### Generate a Flex Web Service Token

1. On the same **Flex Queries** page, scroll down to the **Flex Web Service** section (or look for a separate "Flex Web Service" tab).
2. Click **Create Token** (or **Regenerate** if one already exists).
3. Copy and save the token securely. This token is used as the `IBKR_TOKEN` environment variable.
4. The token is valid for the duration shown. Regenerate it when it expires.

## File Locations

| Path | Purpose | Override |
|------|---------|----------|
| `~/.config/ibctl/config.yaml` | Configuration file | `IBCTL_CONFIG_DIR` |
| `~/.local/share/ibctl/v1/` | Downloaded data cache | `IBCTL_DATA_DIR` |

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `IBKR_TOKEN` | Yes (for `download`) | Your IBKR Flex Web Service token. Never store this in configuration files or commit it to version control. |
| `IBCTL_CONFIG_DIR` | No | Override the configuration directory (default: `~/.config/ibctl`). |
| `IBCTL_DATA_DIR` | No | Override the data directory (default: `~/.local/share/ibctl`). |

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
# IBKR Flex Query configuration (required).
ibkr:
  # The Flex Query ID (required, visible next to your query in IBKR portal).
  query_id: "123456"
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
export IBKR_TOKEN="your-flex-web-service-token"

# Download IBKR data (trades, positions, exchange rates, tax lots).
ibctl download

# View holdings overview.
ibctl holdings overview
ibctl holdings overview --format csv
ibctl holdings overview --format json
```

## Commands

| Command | Description |
|---------|-------------|
| `ibctl config init` | Create a new configuration file and print its path |
| `ibctl config edit` | Edit the configuration file in `$EDITOR` |
| `ibctl config validate` | Validate the configuration file |
| `ibctl download` | Download and cache IBKR data via Flex Query API |
| `ibctl holdings overview` | Display holdings with prices, positions, and classifications |

## Data Storage

All data is cached as protobuf-JSON files under the data directory (`~/.local/share/ibctl/v1/` by default). Each file contains a single protobuf message serialized using `protojson` with proto field names.

| File | Protobuf Message | Description |
|------|-----------------|-------------|
| `trades.json` | `ibctl.data.v1.Trades` | All trades from the IBKR Flex Query. Each trade includes trade ID, date, symbol, buy/sell direction, quantity, price, proceeds, commission, and FIFO realized P&L. |
| `positions.json` | `ibctl.data.v1.Positions` | Open positions as reported by IBKR, including quantity, cost basis price, market price, market value, and unrealized P&L. |
| `tax_lots.json` | `ibctl.data.v1.TaxLots` | FIFO tax lots computed from trades. Each lot tracks symbol, open date, remaining quantity, cost basis price, and long-term status (held >= 1 year). |
| `exchange_rates.json` | `ibctl.data.v1.ExchangeRates` | Currency exchange rates extracted from IBKR cash transactions, supplemented by [frankfurter.dev](https://frankfurter.dev) for any missing dates. |
| `metadata.json` | `ibctl.data.v1.Metadata` | Download timestamp, whether computed positions matched IBKR-reported positions, and any verification discrepancy notes. |

Monetary values use `standard.money.v1.Money` with units and micros (6 decimal places). Dates use `standard.time.v1.Date` with year, month, and day fields.
