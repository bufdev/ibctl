// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctltaxlot provides FIFO tax lot computation for IBKR trades.
package ibctltaxlot

import (
	"errors"
	"fmt"
	"sort"
	"time"

	datav1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/data/v1"
	"github.com/bufdev/ibctl/internal/pkg/moneypb"
	"github.com/bufdev/ibctl/internal/pkg/timepb"
	"github.com/bufdev/ibctl/internal/standard/xtime"
)

// taxLot is an internal representation of a tax lot used during FIFO computation.
type taxLot struct {
	symbol          string
	openDate        xtime.Date
	quantity        int64
	costBasisMicros int64
	currencyCode    string
}

// ComputeTaxLots computes open tax lots from trades using FIFO ordering.
// Buys create new lots, sells consume the oldest lots first.
func ComputeTaxLots(trades []*datav1.Trade) ([]*datav1.TaxLot, error) {
	// Group trades by symbol, sorted by trade date.
	symbolTrades := make(map[string][]*datav1.Trade)
	for _, trade := range trades {
		symbolTrades[trade.GetSymbol()] = append(symbolTrades[trade.GetSymbol()], trade)
	}
	// Sort trades within each symbol by trade date.
	for _, trades := range symbolTrades {
		sort.Slice(trades, func(i, j int) bool {
			dateI := tradeDateString(trades[i])
			dateJ := tradeDateString(trades[j])
			return dateI < dateJ
		})
	}
	// Process trades using FIFO within each symbol.
	symbolLots := make(map[string][]*taxLot)
	for symbol, trades := range symbolTrades {
		for _, trade := range trades {
			switch trade.GetSide() {
			case datav1.TradeSide_TRADE_SIDE_UNSPECIFIED:
				return nil, fmt.Errorf("trade %s has unspecified side", trade.GetTradeId())
			case datav1.TradeSide_TRADE_SIDE_BUY:
				// Parse the trade date for the lot open date.
				openDate, err := protoDateToXtimeDate(trade.GetTradeDate())
				if err != nil {
					return nil, fmt.Errorf("parsing trade date for %s: %w", symbol, err)
				}
				// Create a new tax lot from the buy trade.
				symbolLots[symbol] = append(symbolLots[symbol], &taxLot{
					symbol:          symbol,
					openDate:        openDate,
					quantity:        trade.GetQuantity(),
					costBasisMicros: moneypb.MoneyToMicros(trade.GetTradePrice()),
					currencyCode:    trade.GetCurrencyCode(),
				})
			case datav1.TradeSide_TRADE_SIDE_SELL:
				// Sells consume the oldest lots first (FIFO).
				remainingQuantity := -trade.GetQuantity() // Sell quantity is negative.
				lots := symbolLots[symbol]
				for len(lots) > 0 && remainingQuantity > 0 {
					lot := lots[0]
					if lot.quantity <= remainingQuantity {
						// This lot is fully consumed.
						remainingQuantity -= lot.quantity
						lots = lots[1:]
					} else {
						// This lot is partially consumed.
						lot.quantity -= remainingQuantity
						remainingQuantity = 0
					}
				}
				symbolLots[symbol] = lots
				if remainingQuantity > 0 {
					return nil, fmt.Errorf("insufficient lots for sell of %s: %d shares remaining", symbol, remainingQuantity)
				}
			}
		}
	}
	// Convert internal lots to proto tax lots.
	var result []*datav1.TaxLot
	for _, lots := range symbolLots {
		for _, lot := range lots {
			protoOpenDate, err := timepb.DateToProto(lot.openDate)
			if err != nil {
				return nil, err
			}
			result = append(result, &datav1.TaxLot{
				Symbol:         lot.symbol,
				OpenDate:       protoOpenDate,
				Quantity:       lot.quantity,
				CostBasisPrice: moneypb.MoneyFromMicros(lot.currencyCode, lot.costBasisMicros),
				CurrencyCode:   lot.currencyCode,
			})
		}
	}
	// Sort by symbol then open date for deterministic output.
	sort.Slice(result, func(i, j int) bool {
		if result[i].GetSymbol() != result[j].GetSymbol() {
			return result[i].GetSymbol() < result[j].GetSymbol()
		}
		return taxLotDateString(result[i]) < taxLotDateString(result[j])
	})
	return result, nil
}

// ComputePositions aggregates tax lots into positions with weighted average cost basis.
func ComputePositions(taxLots []*datav1.TaxLot) []*datav1.ComputedPosition {
	// Aggregate quantities and total cost per symbol.
	type symbolData struct {
		quantity        int64
		totalCostMicros int64
		currencyCode    string
	}
	symbolDataMap := make(map[string]*symbolData)
	for _, lot := range taxLots {
		data, ok := symbolDataMap[lot.GetSymbol()]
		if !ok {
			data = &symbolData{currencyCode: lot.GetCurrencyCode()}
			symbolDataMap[lot.GetSymbol()] = data
		}
		data.quantity += lot.GetQuantity()
		// Total cost is price * quantity.
		data.totalCostMicros += moneypb.MoneyToMicros(lot.GetCostBasisPrice()) * lot.GetQuantity()
	}
	// Build computed positions.
	var positions []*datav1.ComputedPosition
	for symbol, data := range symbolDataMap {
		if data.quantity == 0 {
			continue
		}
		// Weighted average cost basis = total cost / total quantity.
		avgCostMicros := data.totalCostMicros / data.quantity
		positions = append(positions, &datav1.ComputedPosition{
			Symbol:                symbol,
			Quantity:              data.quantity,
			AverageCostBasisPrice: moneypb.MoneyFromMicros(data.currencyCode, avgCostMicros),
			CurrencyCode:          data.currencyCode,
		})
	}
	// Sort by symbol for deterministic output.
	sort.Slice(positions, func(i, j int) bool {
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
// Returns a list of discrepancy descriptions, empty if all match.
func VerifyPositions(computed []*datav1.ComputedPosition, reported []*datav1.Position) []string {
	// Build a map of computed positions by symbol.
	computedMap := make(map[string]*datav1.ComputedPosition, len(computed))
	for _, pos := range computed {
		computedMap[pos.GetSymbol()] = pos
	}
	// Build a map of reported positions by symbol.
	reportedMap := make(map[string]*datav1.Position, len(reported))
	for _, pos := range reported {
		reportedMap[pos.GetSymbol()] = pos
	}
	var notes []string
	// Check computed positions against reported.
	for symbol, comp := range computedMap {
		rep, ok := reportedMap[symbol]
		if !ok {
			notes = append(notes, symbol+": computed position exists but not reported by IBKR")
			continue
		}
		if comp.GetQuantity() != rep.GetQuantity() {
			notes = append(notes, fmt.Sprintf("%s: quantity mismatch: computed=%d, reported=%d", symbol, comp.GetQuantity(), rep.GetQuantity()))
		}
		computedAvgPrice := moneypb.MoneyValueToString(comp.GetAverageCostBasisPrice())
		reportedAvgPrice := moneypb.MoneyValueToString(rep.GetCostBasisPrice())
		if computedAvgPrice != reportedAvgPrice {
			notes = append(notes, fmt.Sprintf("%s: average cost basis mismatch: computed=%s, reported=%s", symbol, computedAvgPrice, reportedAvgPrice))
		}
	}
	// Check for positions reported by IBKR but not in computed.
	for symbol := range reportedMap {
		if _, ok := computedMap[symbol]; !ok {
			notes = append(notes, symbol+": reported by IBKR but not in computed positions")
		}
	}
	return notes
}

// tradeDateString returns a sortable date string from a trade's trade_date.
func tradeDateString(trade *datav1.Trade) string {
	if d := trade.GetTradeDate(); d != nil {
		return fmt.Sprintf("%04d-%02d-%02d", d.GetYear(), d.GetMonth(), d.GetDay())
	}
	return ""
}

// taxLotDateString returns a sortable date string from a tax lot's open_date.
func taxLotDateString(lot *datav1.TaxLot) string {
	if d := lot.GetOpenDate(); d != nil {
		return fmt.Sprintf("%04d-%02d-%02d", d.GetYear(), d.GetMonth(), d.GetDay())
	}
	return ""
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
