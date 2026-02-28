# ibctl

A CLI tool for analyzing Interactive Brokers (IBKR) holdings and trades. Downloads data via the IBKR Flex Query API, computes FIFO tax lots, and displays holdings with average prices and positions.

## Setup

### IBKR Flex Query

1. Log in to [Interactive Brokers](https://www.interactivebrokers.com)
2. Go to **Performance & Reports > Flex Queries**
3. Create a new Flex Query that includes:
   - Trades
   - Open Positions
   - Cash Transactions
4. Generate a **Flex Web Service Token** under the same page
5. Note the **Query ID** shown next to your Flex Query

### Configuration

```bash
# Create a default config file
ibctl config init

# Edit ibctl.yaml with your token and query ID
# Then validate it
ibctl config validate
```

## Usage

```bash
# Download IBKR data (trades, positions, exchange rates, tax lots)
ibctl download

# View holdings overview
ibctl holdings overview
ibctl holdings overview --format csv
ibctl holdings overview --format json
```

## Commands

| Command | Description |
|---------|-------------|
| `ibctl config init` | Create a new configuration file with documentation |
| `ibctl config validate` | Validate a configuration file |
| `ibctl download` | Download and cache IBKR data via Flex Query API |
| `ibctl holdings overview` | Display holdings with prices, positions, and classifications |

## Data Storage

All data is cached as JSON files under the configured data directory (`~/Documents/ibctl/v1/` by default):

- `trades.json` — All trades from IBKR
- `positions.json` — Open positions as reported by IBKR
- `tax_lots.json` — FIFO tax lots computed from trades
- `exchange_rates.json` — Currency exchange rates
- `metadata.json` — Download timestamp and position verification results
