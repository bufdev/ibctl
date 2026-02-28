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

	positionv1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/position/v1"
	taxlotv1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/taxlot/v1"
	tradev1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/trade/v1"
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
	currency        string
}

// ComputeTaxLots computes open tax lots from trades using FIFO ordering.
// Buys create new lots, sells consume the oldest lots first.
func ComputeTaxLots(trades []*tradev1.Trade) ([]*taxlotv1.TaxLot, error) {
	// Group trades by symbol, sorted by trade date.
	symbolTrades := make(map[string][]*tradev1.Trade)
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
			switch trade.GetBuySell() {
			case "BUY":
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
					currency:        trade.GetCurrency(),
				})
			case "SELL":
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
	today := xtime.TimeToDate(time.Now())
	var result []*taxlotv1.TaxLot
	for _, lots := range symbolLots {
		for _, lot := range lots {
			protoOpenDate, err := timepb.DateToProto(lot.openDate)
			if err != nil {
				return nil, err
			}
			// Long-term if held >= 365 days.
			longTerm := today.DaysSince(lot.openDate) >= 365
			result = append(result, &taxlotv1.TaxLot{
				Symbol:         lot.symbol,
				OpenDate:       protoOpenDate,
				Quantity:       lot.quantity,
				CostBasisPrice: moneypb.MoneyFromMicros(lot.currency, lot.costBasisMicros),
				Currency:       lot.currency,
				LongTerm:       longTerm,
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
func ComputePositions(taxLots []*taxlotv1.TaxLot) []*taxlotv1.ComputedPosition {
	// Aggregate quantities and total cost per symbol.
	type symbolData struct {
		quantity        int64
		totalCostMicros int64
		currency        string
	}
	symbolDataMap := make(map[string]*symbolData)
	for _, lot := range taxLots {
		data, ok := symbolDataMap[lot.GetSymbol()]
		if !ok {
			data = &symbolData{currency: lot.GetCurrency()}
			symbolDataMap[lot.GetSymbol()] = data
		}
		data.quantity += lot.GetQuantity()
		// Total cost is price * quantity.
		data.totalCostMicros += moneypb.MoneyToMicros(lot.GetCostBasisPrice()) * lot.GetQuantity()
	}
	// Build computed positions.
	var positions []*taxlotv1.ComputedPosition
	for symbol, data := range symbolDataMap {
		if data.quantity == 0 {
			continue
		}
		// Weighted average cost basis = total cost / total quantity.
		avgCostMicros := data.totalCostMicros / data.quantity
		positions = append(positions, &taxlotv1.ComputedPosition{
			Symbol:                symbol,
			Quantity:              data.quantity,
			AverageCostBasisPrice: moneypb.MoneyFromMicros(data.currency, avgCostMicros),
			Currency:              data.currency,
		})
	}
	// Sort by symbol for deterministic output.
	sort.Slice(positions, func(i, j int) bool {
		return positions[i].GetSymbol() < positions[j].GetSymbol()
	})
	return positions
}

// VerifyPositions compares computed positions against IBKR-reported positions.
// Returns a list of discrepancy descriptions, empty if all match.
func VerifyPositions(computed []*taxlotv1.ComputedPosition, reported []*positionv1.Position) []string {
	// Build a map of computed positions by symbol.
	computedMap := make(map[string]*taxlotv1.ComputedPosition, len(computed))
	for _, pos := range computed {
		computedMap[pos.GetSymbol()] = pos
	}
	// Build a map of reported positions by symbol.
	reportedMap := make(map[string]*positionv1.Position, len(reported))
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
func tradeDateString(trade *tradev1.Trade) string {
	if d := trade.GetTradeDate(); d != nil {
		return fmt.Sprintf("%04d-%02d-%02d", d.GetYear(), d.GetMonth(), d.GetDay())
	}
	return ""
}

// taxLotDateString returns a sortable date string from a tax lot's open_date.
func taxLotDateString(lot *taxlotv1.TaxLot) string {
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
}) (xtime.Date, error) {
	if d == nil {
		return xtime.Date{}, errors.New("nil date")
	}
	return xtime.Date{
		Year:  int(d.GetYear()),
		Month: time.Month(d.GetMonth()),
		Day:   int(d.GetDay()),
	}, nil
}
