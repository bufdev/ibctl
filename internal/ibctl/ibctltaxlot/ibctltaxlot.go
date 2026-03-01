// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctltaxlot provides FIFO tax lot computation for IBKR trades.
package ibctltaxlot

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	datav1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/data/v1"
	mathv1 "github.com/bufdev/ibctl/internal/gen/proto/go/standard/math/v1"
	"github.com/bufdev/ibctl/internal/pkg/mathpb"
	"github.com/bufdev/ibctl/internal/pkg/moneypb"
	"github.com/bufdev/ibctl/internal/pkg/timepb"
	"github.com/bufdev/ibctl/internal/standard/xtime"
)

// microsFactor is the number of micros per unit.
const microsFactor = 1_000_000

// costBasisTolerancePct is the percentage threshold below which cost basis
// discrepancies are suppressed. Small differences arise from rounding in
// IBKR's consolidation of order executions vs our FIFO computation.
const costBasisTolerancePct = 0.001

// TaxLotResult contains the output of FIFO tax lot computation.
type TaxLotResult struct {
	// TaxLots is the list of open tax lots after processing all trades.
	TaxLots []*datav1.TaxLot
	// UnmatchedSells records sells that could not be fully matched against
	// existing buy lots (e.g., the buy occurred before the data window).
	UnmatchedSells []UnmatchedSell
}

// UnmatchedSell records a sell trade where the corresponding buy lots
// were not found in the trade data.
type UnmatchedSell struct {
	// AccountAlias is the account alias where the unmatched sell occurred.
	AccountAlias string
	// Symbol is the ticker symbol of the unmatched sell.
	Symbol string
	// UnmatchedQuantity is the remaining quantity that could not be matched.
	UnmatchedQuantity *mathv1.Decimal
}

// PositionDiscrepancy records a mismatch between a computed position and
// the position reported by IBKR.
type PositionDiscrepancy struct {
	// AccountAlias is the account alias where the discrepancy was found.
	AccountAlias string
	// Symbol is the ticker symbol.
	Symbol string
	// Type describes the kind of discrepancy.
	Type DiscrepancyType
	// ComputedValue is the computed value (quantity or price), empty if the
	// position only exists on one side.
	ComputedValue string
	// ReportedValue is the IBKR-reported value, empty if the position only
	// exists on one side.
	ReportedValue string
}

// DiscrepancyType describes the kind of position discrepancy.
type DiscrepancyType int

const (
	// DiscrepancyTypeQuantity indicates a quantity mismatch.
	DiscrepancyTypeQuantity DiscrepancyType = iota + 1
	// DiscrepancyTypeCostBasis indicates an average cost basis mismatch.
	DiscrepancyTypeCostBasis
	// DiscrepancyTypeComputedOnly indicates a position exists in computed data but not in IBKR.
	DiscrepancyTypeComputedOnly
	// DiscrepancyTypeReportedOnly indicates a position exists in IBKR but not in computed data.
	DiscrepancyTypeReportedOnly
)

// lotKey uniquely identifies a group of FIFO lots by account and symbol.
type lotKey struct {
	accountAlias string
	symbol       string
}

// taxLot is an internal representation of a tax lot used during FIFO computation.
// Quantity is stored as total micros (units * 1_000_000 + micros) to support fractional shares.
type taxLot struct {
	accountAlias    string
	symbol          string
	openDate        xtime.Date
	quantityMicros  int64
	costBasisMicros int64
	currencyCode    string
}

// ComputeTaxLots computes open tax lots from trades using FIFO ordering.
// Buys create new lots, sells consume the oldest lots first.
// Trades are grouped by (account_id, symbol) so each account's FIFO is independent.
//
// Transfer-in records are converted to synthetic buy trades before processing.
// Corporate actions (stock splits, etc.) should be pre-processed by the caller
// or handled as synthetic trades.
//
// If a sell cannot be fully matched against existing lots (e.g., the buy
// occurred before the data window), the unmatched quantity is recorded in
// the result rather than failing.
func ComputeTaxLots(trades []*datav1.Trade) (*TaxLotResult, error) {
	// Group trades by (account_id, symbol), sorted by trade date.
	keyTrades := make(map[lotKey][]*datav1.Trade)
	for _, trade := range trades {
		key := lotKey{accountAlias: trade.GetAccountId(), symbol: trade.GetSymbol()}
		keyTrades[key] = append(keyTrades[key], trade)
	}
	// Sort trades within each group by trade date, with buys before sells
	// on the same date. This ensures FIFO has lots available before sells
	// try to consume them (important for same-day buy+sell scenarios).
	for _, trades := range keyTrades {
		sort.Slice(trades, func(i, j int) bool {
			dateI := tradeDateString(trades[i])
			dateJ := tradeDateString(trades[j])
			if dateI != dateJ {
				return dateI < dateJ
			}
			// Within the same date, buys (side=1) come before sells (side=2).
			return trades[i].GetSide() < trades[j].GetSide()
		})
	}
	// Process trades using FIFO within each (account, symbol) group.
	groupLots := make(map[lotKey][]*taxLot)
	var unmatchedSells []UnmatchedSell
	for key, trades := range keyTrades {
		for _, trade := range trades {
			switch trade.GetSide() {
			case datav1.TradeSide_TRADE_SIDE_UNSPECIFIED:
				return nil, fmt.Errorf("trade %s has unspecified side", trade.GetTradeId())
			case datav1.TradeSide_TRADE_SIDE_BUY:
				// Parse the trade date for the lot open date.
				openDate, err := protoDateToXtimeDate(trade.GetTradeDate())
				if err != nil {
					return nil, fmt.Errorf("parsing trade date for %s/%s: %w", key.accountAlias, key.symbol, err)
				}
				tradeQuantityMicros := mathpb.ToMicros(trade.GetQuantity())
				lots := groupLots[key]
				// Check if there are short lots to close (buy-to-close after sell-to-open).
				for len(lots) > 0 && lots[0].quantityMicros < 0 && tradeQuantityMicros > 0 {
					shortLot := lots[0]
					shortQty := -shortLot.quantityMicros // Positive amount to close.
					if shortQty <= tradeQuantityMicros {
						// Fully close this short lot.
						tradeQuantityMicros -= shortQty
						lots = lots[1:]
					} else {
						// Partially close this short lot.
						shortLot.quantityMicros += tradeQuantityMicros
						tradeQuantityMicros = 0
					}
				}
				groupLots[key] = lots
				// Any remaining buy quantity creates a new long lot.
				if tradeQuantityMicros > 0 {
					groupLots[key] = append(groupLots[key], &taxLot{
						accountAlias:    key.accountAlias,
						symbol:          key.symbol,
						openDate:        openDate,
						quantityMicros:  tradeQuantityMicros,
						costBasisMicros: moneypb.MoneyToMicros(trade.GetTradePrice()),
						currencyCode:    trade.GetCurrencyCode(),
					})
				}
			case datav1.TradeSide_TRADE_SIDE_SELL:
				// Sells consume the oldest lots first (FIFO).
				// Sell quantity is negative, so negate to get the positive amount to consume.
				remainingMicros := -mathpb.ToMicros(trade.GetQuantity())
				lots := groupLots[key]
				for len(lots) > 0 && lots[0].quantityMicros > 0 && remainingMicros > 0 {
					lot := lots[0]
					if lot.quantityMicros <= remainingMicros {
						// This lot is fully consumed.
						remainingMicros -= lot.quantityMicros
						lots = lots[1:]
					} else {
						// This lot is partially consumed.
						lot.quantityMicros -= remainingMicros
						remainingMicros = 0
					}
				}
				groupLots[key] = lots
				// If there's remaining sell quantity with no lots to consume,
				// create a short lot (sell-to-open, e.g., writing options).
				if remainingMicros > 0 {
					openDate, err := protoDateToXtimeDate(trade.GetTradeDate())
					if err != nil {
						return nil, fmt.Errorf("parsing trade date for %s/%s: %w", key.accountAlias, key.symbol, err)
					}
					groupLots[key] = append(groupLots[key], &taxLot{
						accountAlias:    key.accountAlias,
						symbol:          key.symbol,
						openDate:        openDate,
						quantityMicros:  -remainingMicros, // Negative = short position.
						costBasisMicros: moneypb.MoneyToMicros(trade.GetTradePrice()),
						currencyCode:    trade.GetCurrencyCode(),
					})
				}
			}
		}
	}
	// Convert internal lots to proto tax lots. All lots (long and short) are included.
	var result []*datav1.TaxLot
	for _, lots := range groupLots {
		for _, lot := range lots {
			// Zero-quantity lots should not exist — they indicate a bug.
			if lot.quantityMicros == 0 {
				return nil, fmt.Errorf("zero-quantity lot for %s/%s on %s: this indicates a FIFO computation bug",
					lot.accountAlias, lot.symbol, lot.openDate)
			}
			protoOpenDate, err := timepb.DateToProto(lot.openDate)
			if err != nil {
				return nil, err
			}
			result = append(result, &datav1.TaxLot{
				Symbol:         lot.symbol,
				AccountId:      lot.accountAlias,
				OpenDate:       protoOpenDate,
				Quantity:       mathpb.FromMicros(lot.quantityMicros),
				CostBasisPrice: moneypb.MoneyFromMicros(lot.currencyCode, lot.costBasisMicros),
				CurrencyCode:   lot.currencyCode,
			})
		}
	}
	// Sort by account, then symbol, then open date for deterministic output.
	sort.Slice(result, func(i, j int) bool {
		if result[i].GetAccountId() != result[j].GetAccountId() {
			return result[i].GetAccountId() < result[j].GetAccountId()
		}
		if result[i].GetSymbol() != result[j].GetSymbol() {
			return result[i].GetSymbol() < result[j].GetSymbol()
		}
		return taxLotDateString(result[i]) < taxLotDateString(result[j])
	})
	return &TaxLotResult{
		TaxLots:        result,
		UnmatchedSells: unmatchedSells,
	}, nil
}

// ComputePositions aggregates tax lots into positions with weighted average cost basis.
// Positions are grouped by (account_id, symbol).
func ComputePositions(taxLots []*datav1.TaxLot) []*datav1.ComputedPosition {
	// Aggregate quantities (as total micros) and total cost per (account, symbol).
	type symbolData struct {
		quantityMicros  int64
		totalCostMicros int64
		currencyCode    string
	}
	dataMap := make(map[lotKey]*symbolData)
	for _, lot := range taxLots {
		key := lotKey{accountAlias: lot.GetAccountId(), symbol: lot.GetSymbol()}
		data, ok := dataMap[key]
		if !ok {
			data = &symbolData{currencyCode: lot.GetCurrencyCode()}
			dataMap[key] = data
		}
		lotQtyMicros := mathpb.ToMicros(lot.GetQuantity())
		data.quantityMicros += lotQtyMicros
		// Total cost = price * quantity. Both are in micros so we need to divide by microsFactor.
		// To avoid int64 overflow with large quantities (e.g., bonds with 331000 face value),
		// divide quantity by microsFactor first (converting back to units), then multiply by price.
		lotQtyUnits := lotQtyMicros / microsFactor
		lotQtyRemainder := lotQtyMicros % microsFactor
		priceMicros := moneypb.MoneyToMicros(lot.GetCostBasisPrice())
		// cost = price * (qtyUnits + qtyRemainder/microsFactor) = price*qtyUnits + price*qtyRemainder/microsFactor
		data.totalCostMicros += priceMicros*lotQtyUnits + priceMicros*lotQtyRemainder/microsFactor
	}
	// Build computed positions.
	var positions []*datav1.ComputedPosition
	for key, data := range dataMap {
		if data.quantityMicros == 0 {
			continue
		}
		// Weighted average cost basis = total cost / total quantity.
		// For large quantities (e.g., bonds with 331000 face value), the naive
		// totalCostMicros * microsFactor overflows int64. Instead, compute
		// avgCost = totalCost / (quantity / microsFactor) for large quantities,
		// falling back to the precise formula for small quantities.
		qtyUnits := data.quantityMicros / microsFactor
		var avgCostMicros int64
		if qtyUnits != 0 {
			// Divide first to avoid overflow: totalCost / qtyUnits gives the result directly.
			avgCostMicros = data.totalCostMicros / qtyUnits
		} else {
			// Small quantity (< 1 unit): use the precise formula.
			avgCostMicros = data.totalCostMicros * microsFactor / data.quantityMicros
		}
		positions = append(positions, &datav1.ComputedPosition{
			Symbol:                key.symbol,
			AccountId:             key.accountAlias,
			Quantity:              mathpb.FromMicros(data.quantityMicros),
			AverageCostBasisPrice: moneypb.MoneyFromMicros(data.currencyCode, avgCostMicros),
			CurrencyCode:          data.currencyCode,
		})
	}
	// Sort by account then symbol for deterministic output.
	sort.Slice(positions, func(i, j int) bool {
		if positions[i].GetAccountId() != positions[j].GetAccountId() {
			return positions[i].GetAccountId() < positions[j].GetAccountId()
		}
		return positions[i].GetSymbol() < positions[j].GetSymbol()
	})
	return positions
}

// IsLongTerm returns whether a tax lot is long-term (held >= 365 days) as of the given date.
func IsLongTerm(lot *datav1.TaxLot, asOf xtime.Date) (bool, error) {
	openDate, err := protoDateToXtimeDate(lot.GetOpenDate())
	if err != nil {
		return false, err
	}
	return asOf.DaysSince(openDate) >= 365, nil
}

// VerifyPositions compares computed positions against IBKR-reported positions.
// Comparison is done per (account_id, symbol). Returns a list of structured discrepancies.
func VerifyPositions(computed []*datav1.ComputedPosition, reported []*datav1.Position) []PositionDiscrepancy {
	// Build a map of computed positions by (account_id, symbol).
	computedMap := make(map[lotKey]*datav1.ComputedPosition, len(computed))
	for _, pos := range computed {
		key := lotKey{accountAlias: pos.GetAccountId(), symbol: pos.GetSymbol()}
		computedMap[key] = pos
	}
	// Build a map of reported positions by (account_id, symbol).
	reportedMap := make(map[lotKey]*datav1.Position, len(reported))
	for _, pos := range reported {
		key := lotKey{accountAlias: pos.GetAccountId(), symbol: pos.GetSymbol()}
		reportedMap[key] = pos
	}
	var discrepancies []PositionDiscrepancy
	// Check computed positions against reported.
	for key, comp := range computedMap {
		rep, ok := reportedMap[key]
		if !ok {
			discrepancies = append(discrepancies, PositionDiscrepancy{
				AccountAlias:  key.accountAlias,
				Symbol:        key.symbol,
				Type:          DiscrepancyTypeComputedOnly,
				ComputedValue: mathpb.ToString(comp.GetQuantity()),
			})
			continue
		}
		compQty := mathpb.ToMicros(comp.GetQuantity())
		repQty := mathpb.ToMicros(rep.GetQuantity())
		if compQty != repQty {
			discrepancies = append(discrepancies, PositionDiscrepancy{
				AccountAlias:  key.accountAlias,
				Symbol:        key.symbol,
				Type:          DiscrepancyTypeQuantity,
				ComputedValue: mathpb.ToString(comp.GetQuantity()),
				ReportedValue: mathpb.ToString(rep.GetQuantity()),
			})
		}
		// Compare cost basis prices numerically. Suppress discrepancies within
		// 0.1% tolerance — small differences arise from rounding in IBKR's
		// consolidation of order executions vs our FIFO computation.
		computedCostMicros := moneypb.MoneyToMicros(comp.GetAverageCostBasisPrice())
		reportedCostMicros := moneypb.MoneyToMicros(rep.GetCostBasisPrice())
		if computedCostMicros != reportedCostMicros {
			absDiff := math.Abs(float64(computedCostMicros - reportedCostMicros))
			absReported := math.Abs(float64(reportedCostMicros))
			// Report only if the difference exceeds the tolerance threshold.
			if absReported == 0 || absDiff/absReported > costBasisTolerancePct {
				discrepancies = append(discrepancies, PositionDiscrepancy{
					AccountAlias:  key.accountAlias,
					Symbol:        key.symbol,
					Type:          DiscrepancyTypeCostBasis,
					ComputedValue: moneypb.MoneyValueToString(comp.GetAverageCostBasisPrice()),
					ReportedValue: moneypb.MoneyValueToString(rep.GetCostBasisPrice()),
				})
			}
		}
	}
	// Check for positions reported by IBKR but not in computed.
	for key, rep := range reportedMap {
		if _, ok := computedMap[key]; !ok {
			discrepancies = append(discrepancies, PositionDiscrepancy{
				AccountAlias:  key.accountAlias,
				Symbol:        key.symbol,
				Type:          DiscrepancyTypeReportedOnly,
				ReportedValue: mathpb.ToString(rep.GetQuantity()),
			})
		}
	}
	return discrepancies
}

// TransfersToSyntheticTrades converts transfer records into synthetic trades
// for FIFO processing. Only transfers with a non-zero transfer price are
// converted — transfers without a price (e.g., FOP transfers from another
// broker) are informational only and the cost basis comes from the IBKR
// position data instead.
func TransfersToSyntheticTrades(transfers []*datav1.Transfer) []*datav1.Trade {
	var trades []*datav1.Trade
	for _, transfer := range transfers {
		// Skip transfers without a transfer price — these are informational
		// (e.g., FOP transfers where cost basis was manually entered in IBKR's
		// Position Transfer Basis tool and is reflected in position data).
		if transfer.GetTransferPrice() == nil {
			continue
		}
		// Skip transfers with a zero transfer price.
		if moneypb.MoneyToMicros(transfer.GetTransferPrice()) == 0 {
			continue
		}
		// Determine trade side from transfer direction.
		var side datav1.TradeSide
		switch transfer.GetDirection() {
		case datav1.TransferDirection_TRANSFER_DIRECTION_IN:
			side = datav1.TradeSide_TRADE_SIDE_BUY
		case datav1.TransferDirection_TRANSFER_DIRECTION_OUT:
			side = datav1.TradeSide_TRADE_SIDE_SELL
		case datav1.TransferDirection_TRANSFER_DIRECTION_UNSPECIFIED:
			// Skip transfers with unknown direction.
			continue
		}
		// Generate a deterministic trade ID for the synthetic trade.
		tradeID := fmt.Sprintf("transfer-%s-%s-%s-%s",
			transfer.GetAccountId(),
			transfer.GetSymbol(),
			protoDateStr(transfer.GetDate()),
			mathpb.ToString(transfer.GetQuantity()),
		)
		trades = append(trades, &datav1.Trade{
			TradeId:       tradeID,
			AccountId:     transfer.GetAccountId(),
			TradeDate:     transfer.GetDate(),
			SettleDate:    transfer.GetDate(),
			Symbol:        transfer.GetSymbol(),
			AssetCategory: transfer.GetAssetCategory(),
			Side:          side,
			Quantity:      transfer.GetQuantity(),
			TradePrice:    transfer.GetTransferPrice(),
			Proceeds:      moneypb.MoneyFromMicros(transfer.GetCurrencyCode(), 0),
			Commission:    moneypb.MoneyFromMicros(transfer.GetCurrencyCode(), 0),
			CurrencyCode:  transfer.GetCurrencyCode(),
		})
	}
	return trades
}

// TradeTransfersToSyntheticTrades converts trade transfer records into
// synthetic buy trades that preserve the original cost basis and holding period.
func TradeTransfersToSyntheticTrades(tradeTransfers []*datav1.TradeTransfer) []*datav1.Trade {
	var trades []*datav1.Trade
	for _, tt := range tradeTransfers {
		// Generate a deterministic trade ID for the synthetic trade.
		tradeID := fmt.Sprintf("trade-transfer-%s-%s-%s-%s",
			tt.GetAccountId(),
			tt.GetSymbol(),
			protoDateStr(tt.GetDate()),
			mathpb.ToString(tt.GetQuantity()),
		)
		// Use orig_trade_date for the lot open date (preserves holding period).
		// Fall back to the transfer date if orig_trade_date is not available.
		tradeDate := tt.GetOrigTradeDate()
		if tradeDate == nil {
			tradeDate = tt.GetDate()
		}
		// Use orig_trade_price as the cost basis, fall back to cost field.
		tradePrice := tt.GetOrigTradePrice()
		if tradePrice == nil {
			tradePrice = tt.GetCost()
		}
		if tradePrice == nil {
			tradePrice = moneypb.MoneyFromMicros(tt.GetCurrencyCode(), 0)
		}
		trades = append(trades, &datav1.Trade{
			TradeId:       tradeID,
			AccountId:     tt.GetAccountId(),
			TradeDate:     tradeDate,
			SettleDate:    tt.GetDate(),
			Symbol:        tt.GetSymbol(),
			AssetCategory: tt.GetAssetCategory(),
			Side:          datav1.TradeSide_TRADE_SIDE_BUY,
			Quantity:      tt.GetQuantity(),
			TradePrice:    tradePrice,
			Proceeds:      moneypb.MoneyFromMicros(tt.GetCurrencyCode(), 0),
			Commission:    moneypb.MoneyFromMicros(tt.GetCurrencyCode(), 0),
			CurrencyCode:  tt.GetCurrencyCode(),
		})
	}
	return trades
}

// *** PRIVATE ***

// tradeDateString returns a sortable date string from a trade's trade_date.
func tradeDateString(trade *datav1.Trade) string {
	return protoDateStr(trade.GetTradeDate())
}

// taxLotDateString returns a sortable date string from a tax lot's open_date.
func taxLotDateString(lot *datav1.TaxLot) string {
	return protoDateStr(lot.GetOpenDate())
}

// protoDateStr returns a sortable date string from a proto Date.
func protoDateStr(d interface {
	GetYear() uint32
	GetMonth() uint32
	GetDay() uint32
}) string {
	if d == nil {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.GetYear(), d.GetMonth(), d.GetDay())
}

// protoDateToXtimeDate converts a proto Date to an xtime.Date.
func protoDateToXtimeDate(d interface {
	GetYear() uint32
	GetMonth() uint32
	GetDay() uint32
},
) (xtime.Date, error) {
	if d == nil {
		return xtime.Date{}, errors.New("nil date")
	}
	return xtime.Date{
		Year:  int(d.GetYear()),
		Month: time.Month(d.GetMonth()),
		Day:   int(d.GetDay()),
	}, nil
}
