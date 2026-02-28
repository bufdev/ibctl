# Decimal Proto Refactor + Stale Docs Cleanup

## Context

The units/micros pattern is duplicated across 6 places in the protos. Create a shared `standard.math.v1.Decimal` message and use it everywhere. Also fix stale references to `~/.config/ibctl` and `IBCTL_CONFIG_DIR` — config now comes from `--config` flag (defaults to `ibctl.yaml`).

## Phase 1: Create `standard.math.v1.Decimal`

**Create** `proto/standard/math/v1/decimal.proto`:
```protobuf
message Decimal {
  int64 units = 1;
  int64 micros = 2 [(buf.validate.field).int64.gte = -999999, (buf.validate.field).int64.lte = 999999];
}
```

With CEL constraints for sign consistency (same as Money has).

## Phase 2: Update Money to use Decimal

**Modify** `proto/standard/money/v1/money.proto`:
- Replace `int64 units = 2` + `int64 micros = 3` with `Decimal value = 2`
- Keep `currency_code = 1`
- This is a wire-breaking change but we're still in early dev

## Phase 3: Update ibctl data protos

Replace all inline units/micros pairs with `Decimal` or `Money`:

- `Trade`: `quantity_units + quantity_micros` → `standard.math.v1.Decimal quantity`
- `Position`: `quantity_units + quantity_micros` → `standard.math.v1.Decimal quantity`
- `TaxLot`: `quantity_units + quantity_micros` → `standard.math.v1.Decimal quantity`
- `ComputedPosition`: `quantity_units + quantity_micros` → `standard.math.v1.Decimal quantity`
- `ExchangeRate`: `rate_units + rate_micros` → `standard.math.v1.Decimal rate`

## Phase 4: Create `internal/pkg/decimalpb/decimalpb.go`

Go helpers for the Decimal proto:
- `NewDecimal(value string) (*Decimal, error)` — parse decimal string
- `DecimalToMicros(d *Decimal) int64` — total micros
- `DecimalFromMicros(totalMicros int64) *Decimal`
- `DecimalToString(d *Decimal) string` — format as decimal string

## Phase 5: Update moneypb to use Decimal

- `Money` now contains `Decimal value` instead of raw units/micros
- Update `NewProtoMoney`, `MoneyToMicros`, `MoneyFromMicros`, `MoneyValueToString`
- Remove `ParseDecimalToUnitsMicros` (moved to decimalpb)

## Phase 6: Update all Go code

- ibctldownload: use `decimalpb.NewDecimal` for quantities
- ibctltaxlot: use `decimalpb.DecimalToMicros` for FIFO math
- ibctlmerge: use `decimalpb.NewDecimal` for CSV quantities
- ibctlholdings: use `decimalpb.DecimalToString` for display

## Phase 7: Fix stale docs

**`cmd/ibctl/main.go`** — Update Long text:
```
Configuration: ibctl.yaml in current directory (override with --config)
Data:          Configured via data_dir in config
```

**`README.md`**:
- File Locations table: remove `~/.config/ibctl/config.yaml` row, replace with `ibctl.yaml (or --config path)`
- Remove `IBCTL_CONFIG_DIR` from env vars table
- Fix data dir reference from `~/.local/share/ibctl/v1/` to `<data_dir>/v1/`

**`internal/ibctl/ibctldownload/ibctldownload.go`** — Remove `~/.local/share/ibctl/v1` from comment

## Verification

1. `make generate && make all` — 0 issues
2. `ibctl download` — handles fractional quantities
3. `ibctl --help` — shows correct config/data paths
