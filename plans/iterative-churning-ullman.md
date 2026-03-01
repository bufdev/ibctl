# Add `ibctl holding lot list <symbol>` command

## Context

The `holding list` command shows aggregated positions. To see the individual FIFO tax lots that make up a position — each with its own open date, cost basis, and holding period — we need a per-lot view. This is essential for tax planning (identifying which lots to sell for STCG vs LTCG treatment).

## Command

```
ibctl holding lot list <symbol> [--dir .] [--format table|csv|json] [--cached]
```

Takes the symbol as a positional argument. Outputs all open tax lots for that symbol across all accounts.

## Output Columns

| Column | Description |
|--------|-------------|
| ACCOUNT | Account alias (e.g., "individual") |
| DATE | Lot open date (YYYY-MM-DD) |
| QUANTITY | Lot quantity |
| CURRENCY | Native currency |
| AVG PRICE | Cost basis price per share (native currency) |
| P&L | Unrealized P&L in native currency: (last price - avg price) × quantity |
| VALUE | Current market value in native currency: last price × quantity |
| AVG USD | Cost basis price in USD |
| P&L USD | P&L converted to USD |
| VALUE USD | Current value in USD |

For bonds (asset category BOND), P&L and VALUE are divided by 100 since prices are percentages of par.

Table format: USD columns formatted with `$` prefix, comma separators, rounded to cents. Totals row at bottom summing P&L USD and VALUE USD.

CSV/JSON: raw decimal values (same pattern as `holding list`).

## New Struct: `LotOverview`

**File**: `internal/ibctl/ibctlholdings/ibctlholdings.go`

```go
type LotOverview struct {
    Account      string          `json:"account"`
    Date         string          `json:"date"`
    Quantity     *mathv1.Decimal `json:"quantity"`
    Currency     string          `json:"currency"`
    AveragePrice string          `json:"average_price"`
    PnL          string          `json:"pnl"`
    Value        string          `json:"value"`
    AverageUSD   string          `json:"average_usd"`
    PnLUSD       string          `json:"pnl_usd"`
    ValueUSD     string          `json:"value_usd"`
}
```

Plus headers, ToRow, ToTableRow functions following the `HoldingOverview` pattern.

## New Function: `GetLotList`

**File**: `internal/ibctl/ibctlholdings/ibctlholdings.go`

```go
func GetLotList(
    symbol string,
    trades []*datav1.Trade,
    positions []*datav1.Position,
    fxStore *ibctlfxrates.Store,
) (*LotListResult, error)
```

Steps:
1. Filter out CASH trades (same as `GetHoldingsOverview`)
2. `ComputeTaxLots(trades)` to get all lots
3. Filter lots by symbol (match across all accounts)
4. Look up last price from positions (by symbol)
5. For each lot: compute P&L and value in native currency, convert to USD via fxStore
6. For bonds: check if position has assetCategory=="BOND", divide by 100
7. Return `LotListResult` with `[]LotOverview` and totals

## Command Files

```
cmd/ibctl/internal/command/holding/
├── holding.go              # Updated: add lot subcommand
├── holdinglist/
│   └── holdinglist.go      # Existing
└── lot/
    ├── lot.go              # New: "lot" command group
    └── lotlist/
        └── lotlist.go      # New: "lot list" leaf command
```

`lotlist.go` follows the same pattern as `holdinglist.go`: --dir, --format, --cached flags, same data loading pipeline, calls `ibctlholdings.GetLotList(symbol, ...)`.

The symbol is a positional argument: `Args: appcmd.ExactArgs(1)`.

## Files to Modify

1. `internal/ibctl/ibctlholdings/ibctlholdings.go` — add LotOverview struct, GetLotList function, headers/row helpers
2. `cmd/ibctl/internal/command/holding/lot/lot.go` — new command group
3. `cmd/ibctl/internal/command/holding/lot/lotlist/lotlist.go` — new leaf command
4. `cmd/ibctl/internal/command/holding/holding.go` — wire lot subcommand

## Verification

1. `go build ./...` and `golangci-lint run ./...` pass
2. `ibctl holding lot list SPY --cached --dir ~/Documents/ibkr` shows individual lots with dates, quantities, P&L
3. `ibctl holding lot list "AMZN 1.65 05/12/28" --cached --dir ~/Documents/ibkr` shows bond lots with /100 adjustment
4. `ibctl holding lot list SPY --format csv` outputs raw CSV
5. `ibctl holding lot list SPY --format json` outputs JSON
6. Totals row: VALUE USD and P&L USD sum correctly
7. Nonexistent symbol returns empty result (no error)
