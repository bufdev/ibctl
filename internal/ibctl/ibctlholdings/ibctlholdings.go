// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctlholdings provides holdings overview computation for ibctl.
package ibctlholdings

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"

	positionv1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/position/v1"
	taxlotv1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/taxlot/v1"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/bufdev/ibctl/internal/ibctl/ibctltaxlot"
	"github.com/bufdev/ibctl/internal/pkg/cli"
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
	// Position is the total number of shares held.
	Position int64 `json:"position"`
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
		strconv.FormatInt(h.Position, 10),
		h.Category,
		h.Type,
		h.Sector,
	}
}

// GetHoldingsOverview reads cached data and computes the holdings overview.
func GetHoldingsOverview(config *ibctlconfig.Config) ([]*HoldingOverview, error) {
	dataDirV1 := config.DataDirV1Path()

	// Read tax lots from cache.
	var taxLotsProto taxlotv1.TaxLots
	taxLotsPath := filepath.Join(dataDirV1, "tax_lots.json")
	if err := cli.ReadProtoMessageJSON(taxLotsPath, &taxLotsProto); err != nil {
		return nil, fmt.Errorf("reading tax lots: %w", err)
	}

	// Read positions from cache (for market prices).
	var positionsProto positionv1.Positions
	positionsPath := filepath.Join(dataDirV1, "positions.json")
	if err := cli.ReadProtoMessageJSON(positionsPath, &positionsProto); err != nil {
		return nil, fmt.Errorf("reading positions: %w", err)
	}

	// Compute positions from tax lots (for average cost basis).
	computedPositions := ibctltaxlot.ComputePositions(taxLotsProto.GetTaxLots())

	// Build a map of market prices from IBKR-reported positions.
	marketPrices := make(map[string]string, len(positionsProto.GetPositions()))
	for _, pos := range positionsProto.GetPositions() {
		marketPrices[pos.GetSymbol()] = moneypb.MoneyValueToString(pos.GetMarketPrice())
	}

	// Build holdings overview from computed positions with metadata from config.
	var holdings []*HoldingOverview
	for _, pos := range computedPositions {
		symbol := pos.GetSymbol()
		holding := &HoldingOverview{
			Symbol:       symbol,
			LastPrice:    marketPrices[symbol],
			AveragePrice: moneypb.MoneyValueToString(pos.GetAverageCostBasisPrice()),
			Position:     pos.GetQuantity(),
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
