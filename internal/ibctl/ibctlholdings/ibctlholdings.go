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
	// MarketValueUSD is position * last price USD.
	MarketValueUSD string `json:"market_value_usd,omitempty"`
	// UnrealizedPnLUSD is (last price USD - avg price USD) * position.
	UnrealizedPnLUSD string `json:"unrealized_pnl_usd,omitempty"`
	// Position is the total quantity held.
	Position *mathv1.Decimal `json:"position"`
	// Category is the user-defined asset category (e.g., "EQUITY").
	Category string `json:"category,omitempty"`
	// Type is the user-defined asset type (e.g., "STOCK", "ETF").
	Type string `json:"type,omitempty"`
	// Sector is the user-defined sector classification (e.g., "TECH").
	Sector string `json:"sector,omitempty"`
	// Geo is the user-defined geographic classification (e.g., "US", "INTL").
	Geo string `json:"geo,omitempty"`
}

// HoldingsOverviewHeaders returns the column headers for table/CSV output.
func HoldingsOverviewHeaders() []string {
	return []string{"SYMBOL", "CURRENCY", "LAST PRICE", "AVG PRICE", "LAST USD", "AVG USD", "MKT VAL USD", "UNRLZD P&L USD", "POSITION", "CATEGORY", "TYPE", "SECTOR", "GEO"}
}

// HoldingOverviewToRow converts a HoldingOverview to a string slice for CSV output.
// USD values are kept as raw decimals for machine-readable output.
func HoldingOverviewToRow(h *HoldingOverview) []string {
	return []string{
		h.Symbol,
		h.Currency,
		h.LastPrice,
		h.AveragePrice,
		h.LastPriceUSD,
		h.AveragePriceUSD,
		h.MarketValueUSD,
		h.UnrealizedPnLUSD,
		mathpb.ToString(h.Position),
		h.Category,
		h.Type,
		h.Sector,
		h.Geo,
	}
}

// HoldingOverviewToTableRow converts a HoldingOverview to a string slice for
// table display. USD values are rounded to cents with $ prefix and comma separators.
func HoldingOverviewToTableRow(h *HoldingOverview) []string {
	return []string{
		h.Symbol,
		h.Currency,
		h.LastPrice,
		h.AveragePrice,
		formatUSD(h.LastPriceUSD),
		formatUSD(h.AveragePriceUSD),
		formatUSD(h.MarketValueUSD),
		formatUSD(h.UnrealizedPnLUSD),
		mathpb.ToString(h.Position),
		h.Category,
		h.Type,
		h.Sector,
		h.Geo,
	}
}

// ComputeTotals sums the MarketValueUSD and UnrealizedPnLUSD across all holdings.
// Returns formatted USD strings for the totals row (rounded to cents with $ prefix).
func ComputeTotals(holdings []*HoldingOverview) (string, string) {
	var totalMktValMicros, totalPnLMicros int64
	for _, h := range holdings {
		if h.MarketValueUSD != "" {
			if units, micros, err := mathpb.ParseToUnitsMicros(h.MarketValueUSD); err == nil {
				totalMktValMicros += units*1_000_000 + micros
			}
		}
		if h.UnrealizedPnLUSD != "" {
			if units, micros, err := mathpb.ParseToUnitsMicros(h.UnrealizedPnLUSD); err == nil {
				totalPnLMicros += units*1_000_000 + micros
			}
		}
	}
	return formatUSD(moneypb.MoneyValueToString(moneypb.MoneyFromMicros("USD", totalMktValMicros))),
		formatUSD(moneypb.MoneyValueToString(moneypb.MoneyFromMicros("USD", totalPnLMicros)))
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
		// Convert prices to USD using the most recent FX rate, then compute
		// market value and unrealized P&L in USD.
		if fxStore != nil {
			var lastPriceUSDMicros, avgPriceUSDMicros int64
			if priceData.money != nil {
				if usdMoney, ok := fxStore.ConvertToUSD(priceData.money.GetMarketPrice()); ok {
					lastPriceUSDMicros = moneypb.MoneyToMicros(usdMoney)
					holding.LastPriceUSD = moneypb.MoneyValueToString(usdMoney)
				}
			}
			if usdMoney, ok := fxStore.ConvertToUSD(avgCostMoney); ok {
				avgPriceUSDMicros = moneypb.MoneyToMicros(usdMoney)
				holding.AveragePriceUSD = moneypb.MoneyValueToString(usdMoney)
			}
			// Market value USD = last price USD * position.
			// Bond prices are percentages of par, so divide by 100 for bonds.
			// Divide quantity first to avoid int64 overflow with large bond face values.
			isBond := priceData.money != nil && priceData.money.GetAssetCategory() == "BOND"
			if lastPriceUSDMicros != 0 {
				qtyRemainder := data.quantityMicros % 1_000_000
				mktValMicros := lastPriceUSDMicros*qtyUnits + lastPriceUSDMicros*qtyRemainder/1_000_000
				if isBond {
					mktValMicros /= 100
				}
				holding.MarketValueUSD = moneypb.MoneyValueToString(moneypb.MoneyFromMicros("USD", mktValMicros))
			}
			// Unrealized P&L USD = (last price USD - avg price USD) * position.
			if lastPriceUSDMicros != 0 && avgPriceUSDMicros != 0 {
				pnlPerShareMicros := lastPriceUSDMicros - avgPriceUSDMicros
				qtyRemainder := data.quantityMicros % 1_000_000
				pnlMicros := pnlPerShareMicros*qtyUnits + pnlPerShareMicros*qtyRemainder/1_000_000
				if isBond {
					pnlMicros /= 100
				}
				holding.UnrealizedPnLUSD = moneypb.MoneyValueToString(moneypb.MoneyFromMicros("USD", pnlMicros))
			}
		}
		// Merge symbol classification from config.
		if symbolConfig, ok := config.SymbolConfigs[symbol]; ok {
			holding.Category = symbolConfig.Category
			holding.Type = symbolConfig.Type
			holding.Sector = symbolConfig.Sector
			holding.Geo = symbolConfig.Geo
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

// *** PRIVATE ***

// formatUSD formats a raw decimal string as a USD value with $ prefix,
// rounded to cents with comma separators (e.g., "$1,234.56", "-$789.01").
// Returns empty string for empty input.
func formatUSD(value string) string {
	if value == "" {
		return ""
	}
	decimal, err := mathpb.NewDecimal(value)
	if err != nil {
		return value
	}
	formatted := mathpb.Format(decimal, 2)
	// Prepend $ after any negative sign.
	if len(formatted) > 0 && formatted[0] == '-' {
		return "-$" + formatted[1:]
	}
	return "$" + formatted
}
