// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctlholdings provides holdings overview computation for ibctl.
//
// Holdings are computed via FIFO tax lot computation from all trade data
// (seed lots from previous brokers + Activity Statement CSVs + Flex Query API).
// Results are verified against IBKR-reported positions to detect discrepancies.
package ibctlholdings

import (
	"sort"

	datav1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/data/v1"
	mathv1 "github.com/bufdev/ibctl/internal/gen/proto/go/standard/math/v1"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlfxrates"
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
	// Currency is the native currency of last price and average price (e.g., "USD", "CAD", "GBP").
	Currency string `json:"currency"`
	// LastPrice is the most recent market price per share in the native currency.
	LastPrice string `json:"last_price"`
	// AveragePrice is the weighted average cost basis price per share in the native currency.
	AveragePrice string `json:"average_price"`
	// LastPriceUSD is the last price converted to USD using the most recent FX rate.
	LastPriceUSD string `json:"last_price_usd,omitempty"`
	// AveragePriceUSD is the average cost basis price converted to USD.
	AveragePriceUSD string `json:"average_price_usd,omitempty"`
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
	return []string{"SYMBOL", "CURRENCY", "LAST PRICE", "AVG PRICE", "LAST USD", "AVG USD", "POSITION", "CATEGORY", "TYPE", "SECTOR"}
}

// HoldingOverviewToRow converts a HoldingOverview to a string slice for table/CSV output.
func HoldingOverviewToRow(h *HoldingOverview) []string {
	return []string{
		h.Symbol,
		h.Currency,
		h.LastPrice,
		h.AveragePrice,
		h.LastPriceUSD,
		h.AveragePriceUSD,
		mathpb.ToString(h.Position),
		h.Category,
		h.Type,
		h.Sector,
	}
}

// GetHoldingsOverview computes the holdings overview from trade data using FIFO,
// then verifies against IBKR-reported positions.
// The result is a combined view aggregated across all accounts.
// The fxStore provides currency conversion for USD price columns.
func GetHoldingsOverview(
	trades []*datav1.Trade,
	positions []*datav1.Position,
	config *ibctlconfig.Config,
	fxStore *ibctlfxrates.Store,
) (*HoldingsResult, error) {
	// Filter out CASH asset category trades (FX conversions like USD.CAD).
	// These are currency exchanges, not security trades.
	var securityTrades []*datav1.Trade
	for _, trade := range trades {
		if trade.GetAssetCategory() == "CASH" {
			continue
		}
		securityTrades = append(securityTrades, trade)
	}
	// Compute FIFO tax lots from all security trades (seed + CSV + Flex Query).
	taxLotResult, err := ibctltaxlot.ComputeTaxLots(securityTrades)
	if err != nil {
		return nil, err
	}
	// Compute per-account positions from tax lots.
	computedPositions := ibctltaxlot.ComputePositions(taxLotResult.TaxLots)
	// Filter out CASH positions from IBKR-reported data before verification.
	var securityPositions []*datav1.Position
	for _, pos := range positions {
		if pos.GetAssetCategory() == "CASH" {
			continue
		}
		securityPositions = append(securityPositions, pos)
	}
	// Verify per-account computed positions against IBKR-reported positions.
	discrepancies := ibctltaxlot.VerifyPositions(computedPositions, securityPositions)

	// Build a map of market prices from IBKR-reported security positions.
	// Stores both the display string and the position proto for FX conversion.
	type marketPriceData struct {
		displayValue string
		money        *datav1.Position
	}
	marketPrices := make(map[string]marketPriceData, len(securityPositions))
	for _, pos := range securityPositions {
		marketPrices[pos.GetSymbol()] = marketPriceData{
			displayValue: moneypb.MoneyValueToString(pos.GetMarketPrice()),
			money:        pos,
		}
	}

	// Aggregate computed positions across accounts for combined display.
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
		// Accumulate total cost for weighted average (price * quantity).
		// Divide quantity first to avoid int64 overflow with large bond quantities.
		priceMicros := moneypb.MoneyToMicros(pos.GetAverageCostBasisPrice())
		qtyUnits := qtyMicros / 1_000_000
		qtyRemainder := qtyMicros % 1_000_000
		data.totalCostMicros += priceMicros*qtyUnits + priceMicros*qtyRemainder/1_000_000
	}

	// Build holdings overview from aggregated positions.
	var holdings []*HoldingOverview
	for symbol, data := range combinedMap {
		if data.quantityMicros == 0 {
			continue
		}
		// Weighted average cost basis = total cost / total quantity.
		// Divide quantity first to avoid int64 overflow with large bond quantities.
		qtyUnits := data.quantityMicros / 1_000_000
		var avgCostMicros int64
		if qtyUnits != 0 {
			avgCostMicros = data.totalCostMicros / qtyUnits
		} else {
			avgCostMicros = data.totalCostMicros * 1_000_000 / data.quantityMicros
		}
		priceData := marketPrices[symbol]
		avgCostMoney := moneypb.MoneyFromMicros(data.currencyCode, avgCostMicros)
		holding := &HoldingOverview{
			Symbol:       symbol,
			Currency:     data.currencyCode,
			LastPrice:    priceData.displayValue,
			AveragePrice: moneypb.MoneyValueToString(avgCostMoney),
			Position:     mathpb.FromMicros(data.quantityMicros),
		}
		// Convert prices to USD using the most recent FX rate.
		if fxStore != nil {
			if priceData.money != nil {
				if usdMoney, ok := fxStore.ConvertToUSD(priceData.money.GetMarketPrice()); ok {
					holding.LastPriceUSD = moneypb.MoneyValueToString(usdMoney)
				}
			}
			if usdMoney, ok := fxStore.ConvertToUSD(avgCostMoney); ok {
				holding.AveragePriceUSD = moneypb.MoneyValueToString(usdMoney)
			}
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
