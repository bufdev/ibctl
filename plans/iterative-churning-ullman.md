# ibctl Implementation Plan

## Context

ibctl is a CLI tool for analyzing Interactive Brokers (IBKR) holdings and trades. It downloads data via the IBKR Flex Query API, computes tax lots using FIFO, and displays holdings with computed average prices and positions. The tool caches all data as JSON files (no database) and supports table/csv/json output.

## API Approach

**Flex Query API** (confirmed by user):
- REST-based, token auth, no local gateway needed
- Two-step: `SendRequest` (get reference code) → `GetStatement` (get XML)
- User creates a Flex Query in the IBKR portal covering Trades, Open Positions, and Cash Transactions
- Config stores the token + query ID

**Exchange rates** (confirmed by user): IBKR data primary, frankfurter.dev API as fallback for missing dates.

## Package Dependency Order

```
cmd/ibctl -> internal/ibctl -> internal/pkg -> internal/standard
```

## Phase 0: Proto Schemas and Standard Library

### 0A: Standard protos

Copy from financemigrate:
- `proto/standard/time/v1/date.proto`
- `proto/standard/money/v1/money.proto`

Delete placeholder:
- `proto/ibctl/foo/v1/foo.proto` and its generated code

### 0B: ibctl proto messages

- `proto/ibctl/trade/v1/trade.proto` — Trade message (trade_id, trade_date, settle_date, symbol, description, asset_category, buy_sell, quantity, trade_price, proceeds, commission, currency, fifo_pnl_realized)
- `proto/ibctl/position/v1/position.proto` — Position message (symbol, description, asset_category, quantity, cost_basis_price, market_price, market_value, fifo_pnl_unrealized, currency). Wrapper message `Positions`.
- `proto/ibctl/taxlot/v1/taxlot.proto` — TaxLot message (symbol, open_date, quantity, cost_basis_price, currency, long_term). ComputedPosition message (symbol, quantity, average_cost_basis_price, currency). Wrapper messages.
- `proto/ibctl/exchangerate/v1/exchangerate.proto` — ExchangeRate message (date, base_currency, quote_currency, rate as string). Wrapper message.
- `proto/ibctl/metadata/v1/metadata.proto` — Metadata message (download_time as string, positions_verified bool, verification_notes repeated string)

All use `standard.money.v1.Money` and `standard.time.v1.Date` where appropriate. Wrapper messages (e.g., `Trades { repeated Trade trades = 1; }`) for top-level JSON serialization.

### 0C: Standard library packages

- `internal/standard/xtime/date.go` — Copy from financemigrate (`/Users/pedge/git/financemigrate/internal/standard/xtime/date.go`), update module path
- `internal/standard/xtime/date_test.go` — Copy from financemigrate, update module path

### 0D: Proto helper packages

- `internal/pkg/timepb/timepb.go` — Adapt from financemigrate. Functions: `NewProtoDate`, `DateToProto`, `ProtoToDate`
- `internal/pkg/moneypb/moneypb.go` — Adapt from financemigrate. Functions: `NewProtoMoney`, `MoneyTimes`, `MoneyValueToString`, add `MoneyToMicros`

**Verify:** `buf lint`, `make generate`, `go test ./internal/standard/...`, `go build ./internal/pkg/...`

## Phase 1: Configuration System

### 1A: `internal/ibctl/ibctlconfig/ibctlconfig.go`

Types:
- `ExternalConfig` — YAML struct: `version`, `data`, `ibkr` (token, query_id), `symbols` (name, category, type, sector)
- `Config` — Validated runtime config: resolved `DataDirPath`, `IBKRToken`, `IBKRQueryID`, `SymbolConfigs` map
- `SymbolConfig` — Category, Type, Sector

Functions:
- `ReadExternalConfig(filePath string) (ExternalConfig, error)` — Parse YAML
- `NewConfig(externalConfig ExternalConfig) (*Config, error)` — Validate (version=v1, data non-empty, ibkr fields non-empty, unique symbol names), resolve `~`
- `InitConfig(filePath string) error` — Write commented template, error if file exists
- `ValidateConfigFile(filePath string) error` — Read + validate
- `(c *Config) DataDirV1Path() string` — Returns `${DataDirPath}/v1`

Config template (hardcoded string with comments since yaml.v3 doesn't emit comments):
```yaml
# The configuration file version.
#
# Required. The only current valid version is v1.
version: v1
# The data directory.
#
# Required.
data: ~/Documents/ibctl
# IBKR Flex Query configuration.
#
# Required. Create a Flex Query at https://www.interactivebrokers.com
# under Performance & Reports > Flex Queries.
ibkr:
  # The Flex Web Service token.
  token: ""
  # The Flex Query ID.
  query_id: ""
# Symbol classification configuration.
#
# Optional. Adds category, type, and sector metadata to holdings output.
# symbols:
#   - name: NET
#     category: EQUITY
#     type: STOCK
#     sector: TECH
```

### 1B: `internal/pkg/cli/cli.go`

Port from financemigrate (`/Users/pedge/git/financemigrate/internal/pkg/cli/cli.go`), add:

- `Format` type (`table`, `csv`, `json`) with `ParseFormat`
- `WriteTable(writer, headers, rows)` — Using `text/tabwriter`
- `WriteCSVRecords(writer, records)` — From financemigrate
- `WriteJSON[O any](writer, objects...)` — From financemigrate
- `WriteProtoMessageJSON(filePath, message)` / `ReadProtoMessageJSON(filePath, message)` — Single proto message file I/O using protojson
- `ForFile`, `ForWriteFile`, `UnmarshalYAMLStrict`, `ExpandHome` — Utilities

### 1C: Config commands

- `cmd/ibctl/internal/command/config/configinit/configinit.go` — `NewCommand`, flags: `--config`, calls `ibctlconfig.InitConfig`
- `cmd/ibctl/internal/command/config/configvalidate/configvalidate.go` — `NewCommand`, flags: `--config`, calls `ibctlconfig.ValidateConfigFile`
- Update `cmd/ibctl/main.go` — Replace analyze placeholder with config sub-command group

Delete:
- `cmd/ibctl/internal/command/analyze/analyze.go`
- `internal/ibctl/ibctl.go`

**Verify:** `ibctl config init` creates file, `ibctl config init` again errors, `ibctl config validate` validates

## Phase 2: API Clients

### 2A: `internal/pkg/flexquery/flexquery.go`

Flex Query API client (not ibctl-specific):

- `Client` interface with `Download(ctx, token, queryID) ([]byte, error)`
- `NewClient(options ...ClientOption) Client`
- `ClientWithHTTPClient` option
- SendRequest URL: `https://ndcdyn.interactivebrokers.com/AccountManagement/FlexWebService/SendRequest`
- GetStatement URL: `https://ndcdyn.interactivebrokers.com/AccountManagement/FlexWebService/GetStatement`
- Query params: `v=3`, `t=<token>`, `q=<queryID/refCode>`
- User-Agent: `"Java"` (IBKR requirement)
- Retry logic for GetStatement (poll until ready, respect rate limits)

### `internal/pkg/flexquery/parse.go`

XML parsing structs:
- `FlexQueryResponse` → `FlexStatements` → `FlexStatement`
- `FlexStatement`: `Trades []XMLTrade`, `OpenPositions []XMLPosition`, `CashTransactions []XMLCashTransaction`
- XML attribute-based structs for Trade, Position, CashTransaction
- `ParseFlexQueryResponse(data []byte) (*FlexQueryResponse, error)`

### 2B: `internal/pkg/fxrate/fxrate.go`

Exchange rate client using frankfurter.dev:
- `Client` interface with `GetRates(ctx, baseCurrency, quoteCurrency, startDate, endDate) (map[string]string, error)`
- `NewClient(options ...ClientOption) Client`
- Endpoint: `GET https://api.frankfurter.dev/v1/{start}..{end}?base={base}&symbols={quote}`

**Verify:** Unit tests with mock HTTP servers

## Phase 3: Download Command

### 3A: `internal/ibctl/ibctltaxlot/ibctltaxlot.go`

Tax lot engine:
- `ComputeTaxLots(trades []*tradev1.Trade) ([]*taxlotv1.TaxLot, error)` — FIFO. Group by symbol, buys create lots, sells consume oldest first. Track holding period (>= 1 year = long_term).
- `ComputePositions(taxLots []*taxlotv1.TaxLot) ([]*taxlotv1.ComputedPosition, error)` — Sum quantities, weighted average price per symbol
- `VerifyPositions(computed, reported) []string` — Compare quantities and average prices, return discrepancy descriptions

Pattern reference: `/Users/pedge/git/financemigrate/internal/fmanalyze/fmanalyze.go` (TaxLots function)

### 3B: `internal/ibctl/ibctldownload/ibctldownload.go`

Download orchestrator:
- `Downloader` interface with `Download(ctx) error`
- `NewDownloader(config, options...) Downloader` with functional options (Logger, FlexQueryClient, FXRateClient)

Download pipeline:
1. Create `${DATA_DIR}/v1/` if needed
2. Fetch XML via flexquery.Client
3. Parse XML via flexquery.ParseFlexQueryResponse
4. Convert XMLTrade → tradev1.Trade protos → write `trades.json`
5. Convert XMLPosition → positionv1.Position protos → write `positions.json`
6. Extract FX rates from CashTransactions + fallback via fxrate.Client → write `exchange_rates.json`
7. Compute tax lots → write `tax_lots.json`
8. Compute positions from tax lots
9. Verify computed vs IBKR-reported positions
10. Write `metadata.json` with timestamp and verification results

### `internal/ibctl/ibctldownload/convert.go`

- `XMLTradeToProto(*flexquery.XMLTrade) (*tradev1.Trade, error)` — Parse string fields to proto using timepb/moneypb
- `XMLPositionToProto(*flexquery.XMLPosition) (*positionv1.Position, error)`

### 3C: Wire download command

- `cmd/ibctl/internal/command/download/download.go` — `NewCommand`, flags: `--config`
- Update `cmd/ibctl/main.go` — Add download sub-command

**Verify:** `ibctl download --config ibctl.yaml` downloads data, writes JSON files to data dir

## Phase 4: Holdings Overview Command

### 4A: `internal/ibctl/ibctlholdings/ibctlholdings.go`

- `HoldingOverview` struct: Symbol, LastPrice, AveragePrice, Position, Category, Type, Sector (json tags)
- `GetHoldingsOverview(config) ([]*HoldingOverview, error)` — Read tax_lots.json and positions.json, merge with config symbol metadata
- `HoldingsOverviewHeaders() []string` and `HoldingOverviewToRow(*HoldingOverview) []string` for table/CSV output

### 4B: Wire holdings overview command

- `cmd/ibctl/internal/command/holdings/holdingsoverview/holdingsoverview.go` — `NewCommand`, flags: `--config`, `--format` (table|csv|json)
- Update `cmd/ibctl/main.go` — Add holdings sub-command group

**Verify:** `ibctl holdings overview --format table`, `--format csv`, `--format json` all produce correct output

## Phase 5: Polish

- Add `Short` descriptions to all commands for `--help`
- Update `README.md` with tool description, IBKR Flex Query setup instructions, usage examples
- `make generate` (no diff), `make lint`, `make test`

## Final Command Tree

```
ibctl
├── config
│   ├── init       # Create new config file with documentation
│   └── validate   # Validate config file
├── download       # Download and cache IBKR data
└── holdings
    └── overview   # Display holdings with prices, positions, classifications
```

## File Manifest

**Create (proto):** `proto/standard/{time,money}/v1/*.proto`, `proto/ibctl/{trade,position,taxlot,exchangerate,metadata}/v1/*.proto`
**Delete (proto):** `proto/ibctl/foo/v1/foo.proto`
**Create (Go):** `internal/standard/xtime/*.go`, `internal/pkg/{timepb,moneypb,cli,flexquery,fxrate}/*.go`, `internal/ibctl/{ibctlconfig,ibctldownload,ibctltaxlot,ibctlholdings}/*.go`, `cmd/ibctl/internal/command/{config/configinit,config/configvalidate,download,holdings/holdingsoverview}/*.go`
**Modify:** `cmd/ibctl/main.go`, `go.mod`, `buf.yaml` (if needed for google/protobuf dep), `README.md`
**Delete (Go):** `cmd/ibctl/internal/command/analyze/analyze.go`, `internal/ibctl/ibctl.go`, `internal/gen/proto/go/ibctl/foo/v1/foo.pb.go`

## Verification

1. `make generate` — no diff in generated code
2. `buf lint` — all protos pass
3. `go vet ./...` — no issues
4. `make lint` — all linters pass
5. `make test` — all tests pass
6. Manual: `ibctl config init`, edit config, `ibctl config validate`, `ibctl download`, `ibctl holdings overview --format table|csv|json`
