// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctlholdings provides holdings overview computation for ibctl.
package ibctlholdings

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	datav1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/data/v1"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/bufdev/ibctl/internal/ibctl/ibctltaxlot"
	"github.com/bufdev/ibctl/internal/pkg/moneypb"
)

// HoldingOverview represents a single holding for display.
type HoldingOverview struct {
	// Symbol is the ticker symbol.
	Symbol string `json:"symbol"`
	// LastPrice is the most recent market price per share.
	LastPrice string `json:"last_price"`
	// AveragePrice is the weighted average cost basis price per share.
	AveragePrice string `json:"average_price"`
	// PositionUnits is the whole units of the total quantity held.
	PositionUnits int64 `json:"position_units"`
	// PositionMicros is the micro units of the total quantity held.
	PositionMicros int64 `json:"position_micros"`
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

// formatQuantity formats units/micros as a decimal string for table/CSV display.
func formatQuantity(units int64, micros int64) string {
	if micros == 0 {
		return strconv.FormatInt(units, 10)
	}
	sign := ""
	if units < 0 || (units == 0 && micros < 0) {
		sign = "-"
		if units < 0 {
			units = -units
		}
		if micros < 0 {
			micros = -micros
		}
	}
	decimalStr := fmt.Sprintf("%06d", micros)
	decimalStr = strings.TrimRight(decimalStr, "0")
	return fmt.Sprintf("%s%d.%s", sign, units, decimalStr)
}

// HoldingOverviewToRow converts a HoldingOverview to a string slice for table/CSV output.
func HoldingOverviewToRow(h *HoldingOverview) []string {
	return []string{
		h.Symbol,
		h.LastPrice,
		h.AveragePrice,
		formatQuantity(h.PositionUnits, h.PositionMicros),
		h.Category,
		h.Type,
		h.Sector,
	}
}

// GetHoldingsOverview computes the holdings overview from merged trade and position data.
// Trades are used to compute FIFO tax lots and average cost basis.
// Positions provide the latest market prices.
func GetHoldingsOverview(trades []*datav1.Trade, positions []*datav1.Position, config *ibctlconfig.Config) ([]*HoldingOverview, error) {
	// Compute tax lots from merged trades.
	taxLots, err := ibctltaxlot.ComputeTaxLots(trades)
	if err != nil {
		return nil, err
	}
	// Compute positions from tax lots (for average cost basis).
	computedPositions := ibctltaxlot.ComputePositions(taxLots)

	// Build a map of market prices from IBKR-reported positions.
	marketPrices := make(map[string]string, len(positions))
	for _, pos := range positions {
		marketPrices[pos.GetSymbol()] = moneypb.MoneyValueToString(pos.GetMarketPrice())
	}

	// Build holdings overview from computed positions with metadata from config.
	var holdings []*HoldingOverview
	for _, pos := range computedPositions {
		symbol := pos.GetSymbol()
		holding := &HoldingOverview{
			Symbol:         symbol,
			LastPrice:      marketPrices[symbol],
			AveragePrice:   moneypb.MoneyValueToString(pos.GetAverageCostBasisPrice()),
			PositionUnits:  pos.GetQuantityUnits(),
			PositionMicros: pos.GetQuantityMicros(),
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
	return holdings, nil
}
