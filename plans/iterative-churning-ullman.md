# FX Rates and USD Price Columns

## Context

Every transaction needs USD and CAD prices computed from FX rates on the transaction date. The current system has 29 IBKR-sourced rates covering only a fraction of needed dates. We need comprehensive daily rate histories for all currency pairs, stored per-pair in the data directory, and used to compute USD prices for the holdings overview.

## Data Directory Reorganization

```
data/v1/
├── accounts/                              # Per-account data (moved from data/v1/)
│   ├── holdco/
│   │   ├── trades.json
│   │   ├── positions.json
│   │   ├── transfers.json
│   │   ├── trade_transfers.json
│   │   └── corporate_actions.json
│   ├── individual/
│   │   └── (same)
│   └── rrsp/
│       └── (same)
└── fx/                                    # FX rate data per currency pair
    ├── USD.CAD/
    │   └── rates.json                     # Daily USD→CAD rates
    ├── GBP.USD/
    │   └── rates.json                     # Daily GBP→USD rates
    ├── EUR.USD/
    │   └── rates.json
    ├── HKD.USD/
    │   └── rates.json
    ├── JPY.USD/
    │   └── rates.json
    ├── GBP.CAD/
    │   └── rates.json                     # For CAD conversion of GBP positions
    ├── EUR.CAD/
    │   └── rates.json
    ├── HKD.CAD/
    │   └── rates.json
    └── JPY.CAD/
        └── rates.json
```

Each `rates.json` is a newline-separated proto JSON file using the existing `ExchangeRate` proto. One entry per date. `provider` field tracks the source (ibkr, frankfurter, bankofcanada).

The old `exchange_rates.json` is removed. The old `data/v1/<account>/` paths move to `data/v1/accounts/<account>/`.

## FX Rate Sources

| Pairs | Source | API |
|-------|--------|-----|
| X→CAD (USD.CAD, EUR.CAD, GBP.CAD, etc.) | Bank of Canada | `https://www.bankofcanada.ca/valet/observations/FX{base}CAD/json?start_date=...&end_date=...` |
| X→USD (EUR.USD, GBP.USD, HKD.USD, JPY.USD) | frankfurter.dev | `https://api.frankfurter.dev/v1/{start}..{end}?base={base}&symbols=USD` |
| IBKR rates | IBKR Flex Query | Extracted from cash transactions (supplementary, not primary) |

## Currency Pairs Needed

Derived from all currencies in the data:
- **USD positions**: no conversion needed for USD column, need USD.CAD for CAD column
- **CAD positions**: need CAD→USD (= 1/USD.CAD), no conversion needed for CAD column
- **GBP positions**: need GBP.USD and GBP.CAD
- **EUR positions**: need EUR.USD and EUR.CAD
- **HKD positions**: need HKD.USD and HKD.CAD
- **JPY positions**: need JPY.USD and JPY.CAD

## New Package: Bank of Canada Client

**File**: `internal/pkg/bankofcanada/bankofcanada.go`

```go
// Client fetches daily FX rates from the Bank of Canada valet API.
type Client interface {
    // GetRates returns daily rates for a currency pair over a date range.
    // The series name format is FX{base}CAD (e.g., FXUSDCAD).
    GetRates(ctx context.Context, baseCurrency string, startDate string, endDate string) (map[string]string, error)
}
```

Response parsing: `observations[].d` for date, `observations[].FX{base}CAD.v` for rate value.

## FX Rate Download

**File**: `internal/ibctl/ibctldownload/ibctldownload.go`

During download:
1. Determine all currencies present across all accounts' trades and seed transactions.
2. For each non-USD currency, download full history for X→USD (frankfurter) and X→CAD (Bank of Canada).
3. For USD→CAD, download from Bank of Canada.
4. Write each pair to `data/v1/fx/{BASE}.{QUOTE}/rates.json`.
5. IBKR rates from cash transactions are merged into the appropriate pair files (ibkr rates supplement but don't override).
6. Eager: download full history from earliest seed transaction date to today.

## New Package: FX Rate Lookup

**File**: `internal/pkg/fxrates/fxrates.go`

```go
// Store provides FX rate lookups from the on-disk rate files.
type Store interface {
    // Rate returns the exchange rate for a currency pair on a given date.
    // Returns an error if the rate is not available.
    Rate(base string, quote string, date xtime.Date) (*mathv1.Decimal, error)
}

// NewStore creates a Store that reads from the fx directory.
func NewStore(fxDirPath string) (Store, error)
```

Loads rate files lazily (on first access per pair). Caches in memory.

## Holdings Overview: USD Columns

**File**: `internal/ibctl/ibctlholdings/ibctlholdings.go`

Add to `HoldingOverview`:
```go
// USDLastPrice is the last price converted to USD.
USDLastPrice string `json:"usd_last_price"`
// USDAveragePrice is the average cost basis converted to USD.
USDAveragePrice string `json:"usd_average_price"`
// USDMarketValue is position * USD last price.
USDMarketValue string `json:"usd_market_value"`
```

`GetHoldingsOverview` accepts the FX rate `Store`. For each position:
- If currency is USD: USD columns = native values.
- If currency is non-USD: look up X→USD rate for today's date, multiply.

Headers: add `USD LAST`, `USD AVG`, `USD VALUE` columns.

## Config Changes

No config changes. The FX data directory is derived from `data_dir` (`data_dir/v1/fx/`).

## Migration

On download, if old `data/v1/<account>/` directories exist (without `accounts/` prefix), move them to `data/v1/accounts/<account>/`. Delete old `exchange_rates.json`.

## Implementation Sequence

1. Reorganize data directory: move account dirs under `accounts/`, create `fx/` dir
2. Create Bank of Canada client
3. Update download to eagerly fetch FX rates per pair into `fx/` directory
4. Create FX rate Store for lookups
5. Update all path references (`dataDirV1Path` → account paths under `accounts/`)
6. Add USD columns to holdings overview
7. Migrate existing data on first download
8. Test: `ibctl holdings overview` shows USD columns with correct conversions

## Verification

1. `make all` passes
2. `ibctl holdings overview` shows USD LAST, USD AVG, USD VALUE columns
3. Non-USD positions (BT.A in GBP, 700 in HKD, 6146.T in JPY) have correct USD conversions
4. USD positions show same values in native and USD columns
5. FX rate files exist for all needed pairs in `data/v1/fx/`
