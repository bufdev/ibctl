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
	"fmt"
	"sort"
	"time"

	datav1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/data/v1"
	mathv1 "github.com/bufdev/ibctl/internal/gen/proto/go/standard/math/v1"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlfxrates"
	"github.com/bufdev/ibctl/internal/ibctl/ibctltaxlot"
	"github.com/bufdev/ibctl/internal/pkg/cliio"
	"github.com/bufdev/ibctl/internal/pkg/mathpb"
	"github.com/bufdev/ibctl/internal/pkg/moneypb"
	"github.com/bufdev/ibctl/internal/standard/xtime"
)

// assetCategoryCash is the IBKR asset category for cash/FX positions.
const assetCategoryCash = "CASH"

// assetCategoryBond is the IBKR asset category for bond positions.
// Bond prices are percentages of par, so market value and P&L are divided by 100.
const assetCategoryBond = "BOND"

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
	// STCGUSD is the short-term (<365 days) unrealized P&L in USD, computed per lot.
	STCGUSD string `json:"stcg_usd,omitempty"`
	// LTCGUSD is the long-term (>=365 days) unrealized P&L in USD, computed per lot.
	LTCGUSD string `json:"ltcg_usd,omitempty"`
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
	return []string{"SYMBOL", "CURRENCY", "LAST PRICE", "AVG PRICE", "LAST USD", "AVG USD", "MKT VAL USD", "UNRLZD P&L USD", "STCG USD", "LTCG USD", "POSITION", "CATEGORY", "TYPE", "SECTOR", "GEO"}
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
		h.STCGUSD,
		h.LTCGUSD,
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
		cliio.FormatUSD(h.LastPriceUSD),
		cliio.FormatUSD(h.AveragePriceUSD),
		cliio.FormatUSD(h.MarketValueUSD),
		cliio.FormatUSD(h.UnrealizedPnLUSD),
		cliio.FormatUSD(h.STCGUSD),
		cliio.FormatUSD(h.LTCGUSD),
		mathpb.ToString(h.Position),
		h.Category,
		h.Type,
		h.Sector,
		h.Geo,
	}
}

// Totals holds the formatted total values for the summary row.
type Totals struct {
	// MarketValueUSD is the total market value across all holdings.
	MarketValueUSD string
	// UnrealizedPnLUSD is the total unrealized P&L across all holdings.
	UnrealizedPnLUSD string
	// STCGUSD is the total short-term unrealized P&L across all holdings.
	STCGUSD string
	// LTCGUSD is the total long-term unrealized P&L across all holdings.
	LTCGUSD string
}

// ComputeTotals sums the USD value columns across all holdings.
// Returns formatted USD strings (rounded to cents with $ prefix).
func ComputeTotals(holdings []*HoldingOverview) *Totals {
	var totalMktValMicros, totalPnLMicros, totalSTCGMicros, totalLTCGMicros int64
	for _, h := range holdings {
		totalMktValMicros += mathpb.ParseMicros(h.MarketValueUSD)
		totalPnLMicros += mathpb.ParseMicros(h.UnrealizedPnLUSD)
		totalSTCGMicros += mathpb.ParseMicros(h.STCGUSD)
		totalLTCGMicros += mathpb.ParseMicros(h.LTCGUSD)
	}
	return &Totals{
		MarketValueUSD:   cliio.FormatUSDMicros(totalMktValMicros),
		UnrealizedPnLUSD: cliio.FormatUSDMicros(totalPnLMicros),
		STCGUSD:          cliio.FormatUSDMicros(totalSTCGMicros),
		LTCGUSD:          cliio.FormatUSDMicros(totalLTCGMicros),
	}
}

// LotListResult contains the lot list output for a single symbol.
type LotListResult struct {
	// Lots is the list of individual tax lots for display.
	Lots []*LotOverview
}

// LotOverview represents a single tax lot for display.
type LotOverview struct {
	// Account is the account alias.
	Account string `json:"account"`
	// Date is the lot open date (YYYY-MM-DD).
	Date string `json:"date"`
	// Quantity is the remaining quantity in this lot.
	Quantity *mathv1.Decimal `json:"quantity"`
	// Currency is the native currency code.
	Currency string `json:"currency"`
	// AveragePrice is the cost basis price per share in native currency.
	AveragePrice string `json:"average_price"`
	// PnL is the unrealized P&L in native currency.
	PnL string `json:"pnl"`
	// Value is the current market value in native currency.
	Value string `json:"value"`
	// AverageUSD is the cost basis price in USD.
	AverageUSD string `json:"average_usd"`
	// PnLUSD is the unrealized P&L in USD.
	PnLUSD string `json:"pnl_usd"`
	// ValueUSD is the current market value in USD.
	ValueUSD string `json:"value_usd"`
}

// LotListHeaders returns the column headers for lot list table/CSV output.
func LotListHeaders() []string {
	return []string{"ACCOUNT", "DATE", "QUANTITY", "CURRENCY", "AVG PRICE", "P&L", "VALUE", "AVG USD", "P&L USD", "VALUE USD"}
}

// LotOverviewToRow converts a LotOverview to a string slice for CSV output.
func LotOverviewToRow(l *LotOverview) []string {
	return []string{
		l.Account,
		l.Date,
		mathpb.ToString(l.Quantity),
		l.Currency,
		l.AveragePrice,
		l.PnL,
		l.Value,
		l.AverageUSD,
		l.PnLUSD,
		l.ValueUSD,
	}
}

// LotOverviewToTableRow converts a LotOverview to a string slice for table display.
// USD columns are formatted with $ prefix, comma separators, rounded to cents.
func LotOverviewToTableRow(l *LotOverview) []string {
	return []string{
		l.Account,
		l.Date,
		mathpb.ToString(l.Quantity),
		l.Currency,
		l.AveragePrice,
		l.PnL,
		l.Value,
		cliio.FormatUSD(l.AverageUSD),
		cliio.FormatUSD(l.PnLUSD),
		cliio.FormatUSD(l.ValueUSD),
	}
}

// ComputeLotTotals sums the P&L USD and VALUE USD across all lots.
// Returns formatted USD strings for the totals row.
func ComputeLotTotals(lots []*LotOverview) (string, string) {
	var totalPnLMicros, totalValueMicros int64
	totalPnLMicros += mathpb.ParseMicros("0")
	for _, l := range lots {
		totalPnLMicros += mathpb.ParseMicros(l.PnLUSD)
		totalValueMicros += mathpb.ParseMicros(l.ValueUSD)
	}
	return cliio.FormatUSDMicros(totalPnLMicros), cliio.FormatUSDMicros(totalValueMicros)
}

// GetLotList returns the individual tax lots for a given symbol.
func GetLotList(
	symbol string,
	trades []*datav1.Trade,
	positions []*datav1.Position,
	fxStore *ibctlfxrates.Store,
) (*LotListResult, error) {
	// Filter out CASH asset category trades.
	var securityTrades []*datav1.Trade
	for _, trade := range trades {
		if trade.GetAssetCategory() == assetCategoryCash {
			continue
		}
		securityTrades = append(securityTrades, trade)
	}
	// Compute FIFO tax lots from all security trades.
	taxLotResult, err := ibctltaxlot.ComputeTaxLots(securityTrades)
	if err != nil {
		return nil, err
	}
	// Look up the last price from IBKR-reported positions for this symbol.
	var lastPriceMoney *datav1.Position
	for _, pos := range positions {
		if pos.GetSymbol() == symbol {
			lastPriceMoney = pos
			break
		}
	}
	// Determine if this is a bond (prices are percentages of par).
	isBond := lastPriceMoney != nil && lastPriceMoney.GetAssetCategory() == assetCategoryBond
	// Get the last price in micros for P&L/value computation.
	var lastPriceMicros int64
	if lastPriceMoney != nil {
		lastPriceMicros = moneypb.MoneyToMicros(lastPriceMoney.GetMarketPrice())
	}
	// Filter lots by symbol and build the lot overview.
	var lots []*LotOverview
	for _, lot := range taxLotResult.TaxLots {
		if lot.GetSymbol() != symbol {
			continue
		}
		costMicros := moneypb.MoneyToMicros(lot.GetCostBasisPrice())
		lotQtyMicros := mathpb.ToMicros(lot.GetQuantity())
		lotQtyUnits := lotQtyMicros / 1_000_000
		lotQtyRemainder := lotQtyMicros % 1_000_000
		currency := lot.GetCurrencyCode()
		// Compute per-lot value and P&L in native currency.
		var valueMicros, pnlMicros int64
		if lastPriceMicros != 0 {
			valueMicros = lastPriceMicros*lotQtyUnits + lastPriceMicros*lotQtyRemainder/1_000_000
			pnlPerUnit := lastPriceMicros - costMicros
			pnlMicros = pnlPerUnit*lotQtyUnits + pnlPerUnit*lotQtyRemainder/1_000_000
			if isBond {
				valueMicros /= 100
				pnlMicros /= 100
			}
		}
		// Format the open date.
		dateStr := ""
		if d := lot.GetOpenDate(); d != nil {
			dateStr = fmt.Sprintf("%04d-%02d-%02d", d.GetYear(), d.GetMonth(), d.GetDay())
		}
		l := &LotOverview{
			Account:      lot.GetAccountId(),
			Date:         dateStr,
			Quantity:     lot.GetQuantity(),
			Currency:     currency,
			AveragePrice: moneypb.MoneyValueToString(lot.GetCostBasisPrice()),
			PnL:          moneypb.MoneyValueToString(moneypb.MoneyFromMicros(currency, pnlMicros)),
			Value:        moneypb.MoneyValueToString(moneypb.MoneyFromMicros(currency, valueMicros)),
		}
		// Convert to USD using FX rates.
		if fxStore != nil {
			if usdCost, ok := fxStore.ConvertToUSD(lot.GetCostBasisPrice()); ok {
				l.AverageUSD = moneypb.MoneyValueToString(usdCost)
			}
			pnlNative := moneypb.MoneyFromMicros(currency, pnlMicros)
			if usdPnL, ok := fxStore.ConvertToUSD(pnlNative); ok {
				l.PnLUSD = moneypb.MoneyValueToString(usdPnL)
			}
			valueNative := moneypb.MoneyFromMicros(currency, valueMicros)
			if usdValue, ok := fxStore.ConvertToUSD(valueNative); ok {
				l.ValueUSD = moneypb.MoneyValueToString(usdValue)
			}
		}
		lots = append(lots, l)
	}
	return &LotListResult{Lots: lots}, nil
}

// GetHoldingsOverview computes the holdings overview from trade data using FIFO,
// then verifies against IBKR-reported positions.
// The result is a combined view aggregated across all accounts.
// The fxStore provides currency conversion for USD price columns.
func GetHoldingsOverview(
	trades []*datav1.Trade,
	positions []*datav1.Position,
	cashPositions []*datav1.CashPosition,
	config *ibctlconfig.Config,
	fxStore *ibctlfxrates.Store,
) (*HoldingsResult, error) {
	// Filter out CASH asset category trades (FX conversions like USD.CAD).
	// These are currency exchanges, not security trades.
	var securityTrades []*datav1.Trade
	for _, trade := range trades {
		if trade.GetAssetCategory() == assetCategoryCash {
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
		if pos.GetAssetCategory() == assetCategoryCash {
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
			isBond := priceData.money != nil && priceData.money.GetAssetCategory() == assetCategoryBond
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

	// Compute per-lot STCG/LTCG split from individual tax lots.
	// Each lot's P&L is classified as short-term (<365 days) or long-term (>=365 days).
	today := xtime.Date{
		Year:  time.Now().Year(),
		Month: time.Now().Month(),
		Day:   time.Now().Day(),
	}
	// Build a map of last price USD micros per symbol for lot-level P&L computation.
	lastPriceUSDMap := make(map[string]int64, len(holdings))
	isBondMap := make(map[string]bool, len(holdings))
	for _, h := range holdings {
		if h.LastPriceUSD != "" {
			lastPriceUSDMap[h.Symbol] = mathpb.ParseMicros(h.LastPriceUSD)
		}
		priceData := marketPrices[h.Symbol]
		isBondMap[h.Symbol] = priceData.money != nil && priceData.money.GetAssetCategory() == assetCategoryBond
	}
	// Accumulate STCG and LTCG per symbol from individual tax lots.
	type gainSplit struct {
		stcgMicros int64
		ltcgMicros int64
	}
	gainsBySymbol := make(map[string]*gainSplit)
	for _, lot := range taxLotResult.TaxLots {
		symbol := lot.GetSymbol()
		lastPriceUSDMicros, ok := lastPriceUSDMap[symbol]
		if !ok || lastPriceUSDMicros == 0 {
			continue
		}
		// Convert lot cost basis to USD.
		costUSDMoney, ok := fxStore.ConvertToUSD(lot.GetCostBasisPrice())
		if !ok {
			continue
		}
		costUSDMicros := moneypb.MoneyToMicros(costUSDMoney)
		// Compute per-lot P&L: (last price USD - cost basis USD) * quantity.
		pnlPerUnitMicros := lastPriceUSDMicros - costUSDMicros
		lotQtyMicros := mathpb.ToMicros(lot.GetQuantity())
		lotQtyUnits := lotQtyMicros / 1_000_000
		lotQtyRemainder := lotQtyMicros % 1_000_000
		lotPnLMicros := pnlPerUnitMicros*lotQtyUnits + pnlPerUnitMicros*lotQtyRemainder/1_000_000
		// Bond prices are percentages of par — divide P&L by 100.
		if isBondMap[symbol] {
			lotPnLMicros /= 100
		}
		// Classify by holding period.
		gs := gainsBySymbol[symbol]
		if gs == nil {
			gs = &gainSplit{}
			gainsBySymbol[symbol] = gs
		}
		longTerm, err := ibctltaxlot.IsLongTerm(lot, today)
		if err != nil {
			continue
		}
		if longTerm {
			gs.ltcgMicros += lotPnLMicros
		} else {
			gs.stcgMicros += lotPnLMicros
		}
	}
	// Apply STCG/LTCG values to holdings.
	for _, h := range holdings {
		gs := gainsBySymbol[h.Symbol]
		if gs == nil {
			continue
		}
		h.STCGUSD = moneypb.MoneyValueToString(moneypb.MoneyFromMicros("USD", gs.stcgMicros))
		h.LTCGUSD = moneypb.MoneyValueToString(moneypb.MoneyFromMicros("USD", gs.ltcgMicros))
	}

	// Append cash positions aggregated by currency across accounts.
	cashByCurrency := make(map[string]int64)
	for _, cp := range cashPositions {
		currency := cp.GetBalance().GetCurrencyCode()
		cashByCurrency[currency] += moneypb.MoneyToMicros(cp.GetBalance())
	}
	for currency, amountMicros := range cashByCurrency {
		if amountMicros == 0 {
			continue
		}
		holding := &HoldingOverview{
			Symbol:   currency,
			Currency: currency,
			// Cash has no market price or cost basis — display "1" as price per unit.
			LastPrice:    "1",
			AveragePrice: "1",
			Position:     mathpb.FromMicros(amountMicros),
			Category:     assetCategoryCash,
		}
		// Convert to USD using FX rates.
		if fxStore != nil {
			balanceMoney := moneypb.MoneyFromMicros(currency, amountMicros)
			if usdMoney, ok := fxStore.ConvertToUSD(balanceMoney); ok {
				usdRate := moneypb.MoneyValueToString(usdMoney)
				// For cash, LastPriceUSD and AvgPriceUSD are the FX rate itself.
				rateMoney := moneypb.MoneyFromMicros(currency, 1_000_000)
				if usdRateMoney, ok := fxStore.ConvertToUSD(rateMoney); ok {
					holding.LastPriceUSD = moneypb.MoneyValueToString(usdRateMoney)
					holding.AveragePriceUSD = moneypb.MoneyValueToString(usdRateMoney)
				}
				holding.MarketValueUSD = usdRate
			}
			// Cash has no unrealized P&L or capital gains.
			holding.UnrealizedPnLUSD = "0"
			holding.STCGUSD = "0"
			holding.LTCGUSD = "0"
		}
		holdings = append(holdings, holding)
	}

	// Append manual cash adjustment holdings from config.
	for currency, adjustmentMicros := range config.CashAdjustments {
		if adjustmentMicros == 0 {
			continue
		}
		holding := &HoldingOverview{
			Symbol:       currency + " ADJUSTMENT",
			Currency:     currency,
			LastPrice:    "1",
			AveragePrice: "1",
			Position:     mathpb.FromMicros(adjustmentMicros),
			Category:     assetCategoryCash,
		}
		// Convert to USD using FX rates.
		if fxStore != nil {
			balanceMoney := moneypb.MoneyFromMicros(currency, adjustmentMicros)
			if usdMoney, ok := fxStore.ConvertToUSD(balanceMoney); ok {
				rateMoney := moneypb.MoneyFromMicros(currency, 1_000_000)
				if usdRateMoney, ok := fxStore.ConvertToUSD(rateMoney); ok {
					holding.LastPriceUSD = moneypb.MoneyValueToString(usdRateMoney)
					holding.AveragePriceUSD = moneypb.MoneyValueToString(usdRateMoney)
				}
				holding.MarketValueUSD = moneypb.MoneyValueToString(usdMoney)
			}
			holding.UnrealizedPnLUSD = "0"
			holding.STCGUSD = "0"
			holding.LTCGUSD = "0"
		}
		holdings = append(holdings, holding)
	}

	// Sort by category (cash last) then symbol for deterministic output.
	sort.Slice(holdings, func(i, j int) bool {
		// Cash positions sort after all other categories.
		iCash := holdings[i].Category == assetCategoryCash
		jCash := holdings[j].Category == assetCategoryCash
		if iCash != jCash {
			return !iCash
		}
		return holdings[i].Symbol < holdings[j].Symbol
	})
	return &HoldingsResult{
		Holdings:              holdings,
		UnmatchedSells:        taxLotResult.UnmatchedSells,
		PositionDiscrepancies: discrepancies,
	}, nil
}
