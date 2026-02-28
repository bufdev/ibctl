// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctlholdings provides holdings overview computation for ibctl.
package ibctlholdings

import (
	"sort"

	datav1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/data/v1"
	mathv1 "github.com/bufdev/ibctl/internal/gen/proto/go/standard/math/v1"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/bufdev/ibctl/internal/ibctl/ibctltaxlot"
	"github.com/bufdev/ibctl/internal/pkg/mathpb"
	"github.com/bufdev/ibctl/internal/pkg/moneypb"
)

// HoldingsResult contains the holdings overview along with any data
// inconsistencies detected during computation.
type HoldingsResult struct {
	// Holdings is the list of holdings for display.
	Holdings []*HoldingOverview
	// UnmatchedSells records sells that could not be matched to buy lots.
	UnmatchedSells []ibctltaxlot.UnmatchedSell
	// PositionDiscrepancies records mismatches between computed and IBKR-reported positions.
	PositionDiscrepancies []ibctltaxlot.PositionDiscrepancy
}

// HoldingOverview represents a single holding for display.
type HoldingOverview struct {
	// Symbol is the ticker symbol.
	Symbol string `json:"symbol"`
	// LastPrice is the most recent market price per share.
	LastPrice string `json:"last_price"`
	// AveragePrice is the weighted average cost basis price per share.
	AveragePrice string `json:"average_price"`
	// Position is the total quantity held.
	Position *mathv1.Decimal `json:"position"`
	// Category is the user-defined asset category (e.g., "EQUITY").
	Category string `json:"category,omitempty"`
	// Type is the user-defined asset type (e.g., "STOCK", "ETF").
	Type string `json:"type,omitempty"`
	// Sector is the user-defined sector classification (e.g., "TECH").
	Sector string `json:"sector,omitempty"`
}

// HoldingsOverviewHeaders returns the column headers for table/CSV output.
func HoldingsOverviewHeaders() []string {
	return []string{"SYMBOL", "LAST PRICE", "AVG PRICE", "POSITION", "CATEGORY", "TYPE", "SECTOR"}
}

// HoldingOverviewToRow converts a HoldingOverview to a string slice for table/CSV output.
func HoldingOverviewToRow(h *HoldingOverview) []string {
	return []string{
		h.Symbol,
		h.LastPrice,
		h.AveragePrice,
		mathpb.ToString(h.Position),
		h.Category,
		h.Type,
		h.Sector,
	}
}

// GetHoldingsOverview computes the holdings overview from merged trade and position data.
// Trades are used to compute FIFO tax lots and average cost basis.
// Positions provide the latest market prices and are used for verification.
// Transfers are converted to synthetic trades for FIFO processing.
// The result is a combined view aggregated across all accounts.
func GetHoldingsOverview(
	trades []*datav1.Trade,
	positions []*datav1.Position,
	transfers []*datav1.Transfer,
	tradeTransfers []*datav1.TradeTransfer,
	config *ibctlconfig.Config,
) (*HoldingsResult, error) {
	// Convert transfers and trade transfers to synthetic trades for FIFO processing.
	allTrades := make([]*datav1.Trade, 0, len(trades))
	allTrades = append(allTrades, trades...)
	allTrades = append(allTrades, ibctltaxlot.TransfersToSyntheticTrades(transfers)...)
	allTrades = append(allTrades, ibctltaxlot.TradeTransfersToSyntheticTrades(tradeTransfers)...)
	// Compute tax lots from all trades (real + synthetic from transfers).
	taxLotResult, err := ibctltaxlot.ComputeTaxLots(allTrades)
	if err != nil {
		return nil, err
	}
	// Compute per-account positions from tax lots.
	computedPositions := ibctltaxlot.ComputePositions(taxLotResult.TaxLots)
	// Verify computed positions against IBKR-reported positions.
	discrepancies := ibctltaxlot.VerifyPositions(computedPositions, positions)

	// Build a map of market prices from IBKR-reported positions, keyed by symbol.
	// For combined view, we use the latest price from any account that reports it.
	marketPrices := make(map[string]string, len(positions))
	for _, pos := range positions {
		marketPrices[pos.GetSymbol()] = moneypb.MoneyValueToString(pos.GetMarketPrice())
	}

	// Aggregate computed positions across accounts for combined view.
	// Group by symbol, sum quantities, weighted-average cost basis.
	type combinedData struct {
		quantityMicros  int64
		totalCostMicros int64
		currencyCode    string
	}
	combinedMap := make(map[string]*combinedData)
	for _, pos := range computedPositions {
		symbol := pos.GetSymbol()
		data, ok := combinedMap[symbol]
		if !ok {
			data = &combinedData{currencyCode: pos.GetCurrencyCode()}
			combinedMap[symbol] = data
		}
		qtyMicros := mathpb.ToMicros(pos.GetQuantity())
		data.quantityMicros += qtyMicros
		// Accumulate total cost for weighted average.
		data.totalCostMicros += moneypb.MoneyToMicros(pos.GetAverageCostBasisPrice()) * qtyMicros / 1_000_000
	}

	// Build holdings overview from aggregated positions.
	var holdings []*HoldingOverview
	for symbol, data := range combinedMap {
		if data.quantityMicros == 0 {
			continue
		}
		// Weighted average cost basis = total cost / total quantity.
		avgCostMicros := data.totalCostMicros * 1_000_000 / data.quantityMicros
		holding := &HoldingOverview{
			Symbol:       symbol,
			LastPrice:    marketPrices[symbol],
			AveragePrice: moneypb.MoneyValueToString(moneypb.MoneyFromMicros(data.currencyCode, avgCostMicros)),
			Position:     mathpb.FromMicros(data.quantityMicros),
		}
		// Merge symbol classification from config.
		if symbolConfig, ok := config.SymbolConfigs[symbol]; ok {
			holding.Category = symbolConfig.Category
			holding.Type = symbolConfig.Type
			holding.Sector = symbolConfig.Sector
		}
		holdings = append(holdings, holding)
	}

	// Sort by symbol for deterministic output.
	sort.Slice(holdings, func(i, j int) bool {
		return holdings[i].Symbol < holdings[j].Symbol
	})
	return &HoldingsResult{
		Holdings:              holdings,
		UnmatchedSells:        taxLotResult.UnmatchedSells,
		PositionDiscrepancies: discrepancies,
	}, nil
}
