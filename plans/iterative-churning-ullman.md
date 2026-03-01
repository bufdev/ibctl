# Import Pre-Transfer Tax Lots and Fix Holdings Computation

## Context

`ibctl holdings overview` produces wrong results because many positions in the individual account were transferred in from UBS via FOP transfers. IBKR has no buy trades for these — the buys happened at UBS/RBC. The Flex Query API does not expose lot-level data.

The `financemigrate` project computed tax lots from the old broker data. Its output — `tax_lots.csv` — contains **249 individual tax lots** with acquisition dates, per-lot cost basis, and quantities across 68 symbols. This covers 87 of 89 FOP transfers with matching quantities.

The plan:
1. Build an `ibctl import` command that reads the external CSV **once**, maps symbols, and writes the data into ibctl's own data directory as protobuf-JSON (never references the source file again)
2. The imported data lives at `<data_dir>/v1/<account>/imported_lots.json`
3. Restore FIFO-based holdings computation using imported lots + IBKR trades + CSV trades
4. Verify against IBKR-reported positions

## Data Source (One-Time Import)

**File**: `~/Documents/ubs_to_ibkr/Data/tax_lots.csv` (consumed once, then never referenced again)

```
Date,Symbol,Type,Quantity,Price,Total,Currency
02/27/2025,0700.HK,BUY,400,477.800000,191120.000000,HKD
04/09/2024,AAPL,BUY,337,169.330000,57064.210000,USD
```

- 249 rows, all Type=BUY
- Date = acquisition date (preserves holding period)
- Price = per-share cost basis in the position's currency
- Symbols use exchange suffixes that differ from IBKR symbols

## Config Changes

**File**: `internal/ibctl/ibctlconfig/ibctlconfig.go`

Add `seed_dir` to `ExternalConfigV1` — path to the permanent seed data directory. Optional (empty means no seed data).

```yaml
# Permanent seed data directory — pre-transfer tax lots from previous brokers.
# Organized by account subdirectory (e.g., seed_dir/individual/lots.json).
# This data is manually curated and must not be deleted.
seed_dir: ~/Documents/ibkr/seed
```

Runtime `Config` gets `SeedDirPath string` (resolved via `xos.ExpandHome`, empty if not configured).

No changes to accounts structure (stays as `map[string]string` for now).

## Seed Data (Written During Implementation, No User Action)

During implementation, I will write the seed data directly:
1. Read `~/Documents/ubs_to_ibkr/Data/tax_lots.csv`
2. Apply the symbol mappings (hardcoded in the conversion script, not in config)
3. Convert each row to a `datav1.Trade` proto (with `account_id`, `trade_date`, `quantity`, `trade_price`, etc.)
4. Write to `<seed_dir>/individual/lots.json` as newline-separated proto-JSON

A new `seed_dir` config field (separate from `data_dir`) points to permanent seed data that must not be deleted. `data_dir` is cache that can be re-downloaded; `seed_dir` is manually curated historical data.

```yaml
# Permanent seed data directory — pre-transfer tax lots from previous brokers.
# This data is manually curated and must not be deleted.
seed_dir: ~/Documents/ibkr/seed
```

```
~/Documents/ibkr/seed/individual/lots.json   # 249 pre-transfer lots as Trade protos
```

## Updated Merge: Include Seed Lots

**File**: `internal/ibctl/ibctlmerge/ibctlmerge.go`

`Merge` accepts `seedDirPath` from config. For each account, read `<seed_dir>/<account>/lots.json` if it exists and include those trades. They're already in proto Trade format, so no conversion needed — just add to the trade list.

Since seed lots have the earliest dates (pre-transfer acquisitions), FIFO ordering naturally puts them first.

## Holdings: Back to FIFO Computation

**File**: `internal/ibctl/ibctlholdings/ibctlholdings.go`

Restore FIFO-based computation with verification:
1. Compute tax lots from all trades (imported + CSV + Flex Query)
2. Compute positions from tax lots
3. Verify against IBKR-reported positions
4. Log discrepancies as warnings
5. Display combined holdings

The key difference from before: with imported lots providing the missing buy-side data, FIFO should produce correct results.

## Verification

I will verify before presenting results to the user:

1. `make all` passes
2. `ibctl holdings overview` compared against `~/Downloads/holdings.csv`:
   - Every symbol in truth file appears in output
   - Quantities match exactly
   - Cost basis prices close (minor rounding acceptable)
3. Unmatched sell warnings minimal
4. "Position reported by IBKR but not in computed data" warnings zero for equities

## Implementation Sequence

1. Write seed data: python3 script reads tax_lots.csv, maps symbols, writes proto-JSON to `~/Documents/ibkr/seed/individual/lots.json`
2. Config: add `seed_dir` field
3. Merge: accept `seedDirPath`, read `<seed_dir>/<account>/lots.json` per account
4. Holdings: restore FIFO computation with verification against IBKR positions
5. Test: compare `ibctl holdings overview` against `~/Downloads/holdings.csv`, iterate until match
6. README: document seed data directory and its purpose
