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
	// Symbol is the ticker symbol.
	Symbol string `json:"symbol"`
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
	// STCGUSD is the short-term P&L in USD (held < 365 days). Equals PnLUSD or 0.
	STCGUSD string `json:"stcg_usd"`
	// LTCGUSD is the long-term P&L in USD (held >= 365 days). Equals PnLUSD or 0.
	LTCGUSD string `json:"ltcg_usd"`
	// Category is the user-defined asset category (e.g., "EQUITY").
	Category string `json:"category,omitempty"`
	// Type is the user-defined asset type (e.g., "STOCK", "ETF").
	Type string `json:"type,omitempty"`
	// Sector is the user-defined sector classification (e.g., "TECH").
	Sector string `json:"sector,omitempty"`
	// Geo is the user-defined geographic classification (e.g., "US", "INTL").
	Geo string `json:"geo,omitempty"`
}

// LotListHeaders returns the column headers for lot list table/CSV output.
func LotListHeaders() []string {
	return []string{"SYMBOL", "ACCOUNT", "DATE", "QUANTITY", "CURRENCY", "AVG PRICE", "P&L", "VALUE", "AVG USD", "P&L USD", "STCG USD", "LTCG USD", "VALUE USD", "CATEGORY", "TYPE", "SECTOR", "GEO"}
}

// LotOverviewToRow converts a LotOverview to a string slice for CSV output.
func LotOverviewToRow(l *LotOverview) []string {
	return []string{
		l.Symbol,
		l.Account,
		l.Date,
		mathpb.ToString(l.Quantity),
		l.Currency,
		l.AveragePrice,
		l.PnL,
		l.Value,
		l.AverageUSD,
		l.PnLUSD,
		l.STCGUSD,
		l.LTCGUSD,
		l.ValueUSD,
		l.Category,
		l.Type,
		l.Sector,
		l.Geo,
	}
}

// LotOverviewToTableRow converts a LotOverview to a string slice for table display.
// USD columns are formatted with $ prefix, comma separators, rounded to cents.
func LotOverviewToTableRow(l *LotOverview) []string {
	return []string{
		l.Symbol,
		l.Account,
		l.Date,
		mathpb.ToString(l.Quantity),
		l.Currency,
		l.AveragePrice,
		l.PnL,
		l.Value,
		cliio.FormatUSD(l.AverageUSD),
		cliio.FormatUSD(l.PnLUSD),
		cliio.FormatUSD(l.STCGUSD),
		cliio.FormatUSD(l.LTCGUSD),
		cliio.FormatUSD(l.ValueUSD),
		l.Category,
		l.Type,
		l.Sector,
		l.Geo,
	}
}

// LotTotals holds the formatted total values for the lot list summary row.
type LotTotals struct {
	// PnLUSD is the total unrealized P&L in USD.
	PnLUSD string
	// ValueUSD is the total market value in USD.
	ValueUSD string
	// STCGUSD is the total short-term P&L in USD.
	STCGUSD string
	// LTCGUSD is the total long-term P&L in USD.
	LTCGUSD string
}

// ComputeLotTotals sums the USD value columns across all lots.
func ComputeLotTotals(lots []*LotOverview) *LotTotals {
	var totalPnLMicros, totalValueMicros, totalSTCGMicros, totalLTCGMicros int64
	for _, l := range lots {
		totalPnLMicros += mathpb.ParseMicros(l.PnLUSD)
		totalValueMicros += mathpb.ParseMicros(l.ValueUSD)
		totalSTCGMicros += mathpb.ParseMicros(l.STCGUSD)
		totalLTCGMicros += mathpb.ParseMicros(l.LTCGUSD)
	}
	return &LotTotals{
		PnLUSD:   cliio.FormatUSDMicros(totalPnLMicros),
		ValueUSD: cliio.FormatUSDMicros(totalValueMicros),
		STCGUSD:  cliio.FormatUSDMicros(totalSTCGMicros),
		LTCGUSD:  cliio.FormatUSDMicros(totalLTCGMicros),
	}
}

// CategoryOverview represents holdings aggregated by category.
type CategoryOverview struct {
	// Category is the asset category (e.g., "EQUITY", "FIXED_INCOME", "CASH").
	Category string `json:"category"`
	// MarketValueUSD is the total market value in USD.
	MarketValueUSD string `json:"market_value_usd"`
	// NetLiqPct is the percentage of total portfolio value (e.g., "45.23%").
	NetLiqPct string `json:"net_liq_pct"`
	// UnrealizedPnLUSD is the total unrealized P&L in USD.
	UnrealizedPnLUSD string `json:"unrealized_pnl_usd"`
	// STCGUSD is the total short-term P&L in USD.
	STCGUSD string `json:"stcg_usd"`
	// LTCGUSD is the total long-term P&L in USD.
	LTCGUSD string `json:"ltcg_usd"`
}

// CategoryListHeaders returns the column headers for category list output.
func CategoryListHeaders() []string {
	return []string{"CATEGORY", "MKT VAL USD", "NET LIQ %", "UNRLZD P&L USD", "STCG USD", "LTCG USD"}
}

// CategoryOverviewToRow converts a CategoryOverview to a string slice for CSV output.
func CategoryOverviewToRow(c *CategoryOverview) []string {
	return []string{
		c.Category,
		c.MarketValueUSD,
		c.NetLiqPct,
		c.UnrealizedPnLUSD,
		c.STCGUSD,
		c.LTCGUSD,
	}
}

// CategoryOverviewToTableRow converts a CategoryOverview to a string slice for table display.
func CategoryOverviewToTableRow(c *CategoryOverview) []string {
	return []string{
		c.Category,
		cliio.FormatUSD(c.MarketValueUSD),
		c.NetLiqPct,
		cliio.FormatUSD(c.UnrealizedPnLUSD),
		cliio.FormatUSD(c.STCGUSD),
		cliio.FormatUSD(c.LTCGUSD),
	}
}

// GetCategoryList aggregates holdings by category from a HoldingsResult.
func GetCategoryList(holdings []*HoldingOverview) []*CategoryOverview {
	// Accumulate per-category totals in micros.
	type categoryData struct {
		mktValMicros int64
		pnlMicros    int64
		stcgMicros   int64
		ltcgMicros   int64
	}
	dataMap := make(map[string]*categoryData)
	var totalMktValMicros int64
	for _, h := range holdings {
		cat := h.Category
		if cat == "" {
			cat = "UNCATEGORIZED"
		}
		data, ok := dataMap[cat]
		if !ok {
			data = &categoryData{}
			dataMap[cat] = data
		}
		mktVal := mathpb.ParseMicros(h.MarketValueUSD)
		data.mktValMicros += mktVal
		data.pnlMicros += mathpb.ParseMicros(h.UnrealizedPnLUSD)
		data.stcgMicros += mathpb.ParseMicros(h.STCGUSD)
		data.ltcgMicros += mathpb.ParseMicros(h.LTCGUSD)
		totalMktValMicros += mktVal
	}
	// Build category overview entries with net liq percentage.
	var categories []*CategoryOverview
	for cat, data := range dataMap {
		// Compute net liq percentage: mkt val / total mkt val * 100.
		var pctStr string
		if totalMktValMicros != 0 {
			pct := float64(data.mktValMicros) / float64(totalMktValMicros) * 100
			pctStr = fmt.Sprintf("%.2f%%", pct)
		}
		categories = append(categories, &CategoryOverview{
			Category:         cat,
			MarketValueUSD:   moneypb.MoneyValueToString(moneypb.MoneyFromMicros("USD", data.mktValMicros)),
			NetLiqPct:        pctStr,
			UnrealizedPnLUSD: moneypb.MoneyValueToString(moneypb.MoneyFromMicros("USD", data.pnlMicros)),
			STCGUSD:          moneypb.MoneyValueToString(moneypb.MoneyFromMicros("USD", data.stcgMicros)),
			LTCGUSD:          moneypb.MoneyValueToString(moneypb.MoneyFromMicros("USD", data.ltcgMicros)),
		})
	}
	// Sort by category name for deterministic output.
	sort.Slice(categories, func(i, j int) bool {
		return categories[i].Category < categories[j].Category
	})
	return categories
}

// GetLotList returns individual tax lots, optionally filtered by symbol.
// If symbol is empty, all lots are returned.
func GetLotList(
	symbol string,
	trades []*datav1.Trade,
	positions []*datav1.Position,
	config *ibctlconfig.Config,
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
	// Build a map of last prices and bond status from IBKR-reported positions.
	type positionData struct {
		lastPriceMicros int64
		isBond          bool
	}
	positionMap := make(map[string]*positionData, len(positions))
	for _, pos := range positions {
		if pos.GetAssetCategory() == assetCategoryCash {
			continue
		}
		positionMap[pos.GetSymbol()] = &positionData{
			lastPriceMicros: moneypb.MoneyToMicros(pos.GetMarketPrice()),
			isBond:          pos.GetAssetCategory() == assetCategoryBond,
		}
	}
	// Compute today's date for holding period classification.
	today := xtime.Date{
		Year:  time.Now().Year(),
		Month: time.Now().Month(),
		Day:   time.Now().Day(),
	}
	// Build the lot overview, optionally filtering by symbol.
	var lots []*LotOverview
	for _, lot := range taxLotResult.TaxLots {
		lotSymbol := lot.GetSymbol()
		// Filter by symbol if specified.
		if symbol != "" && lotSymbol != symbol {
			continue
		}
		// Look up position data for this symbol.
		pd := positionMap[lotSymbol]
		var lastPriceMicros int64
		var isBond bool
		if pd != nil {
			lastPriceMicros = pd.lastPriceMicros
			isBond = pd.isBond
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
			Symbol:       lotSymbol,
			Account:      lot.GetAccountId(),
			Date:         dateStr,
			Quantity:     lot.GetQuantity(),
			Currency:     currency,
			AveragePrice: moneypb.MoneyValueToString(lot.GetCostBasisPrice()),
			PnL:          moneypb.MoneyValueToString(moneypb.MoneyFromMicros(currency, pnlMicros)),
			Value:        moneypb.MoneyValueToString(moneypb.MoneyFromMicros(currency, valueMicros)),
		}
		// Merge symbol classification from config.
		if symbolConfig, ok := config.SymbolConfigs[lotSymbol]; ok {
			l.Category = symbolConfig.Category
			l.Type = symbolConfig.Type
			l.Sector = symbolConfig.Sector
			l.Geo = symbolConfig.Geo
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
		// Classify P&L as short-term or long-term based on holding period.
		// For a single lot, the entire P&L is one or the other.
		if l.PnLUSD != "" {
			longTerm, ltErr := ibctltaxlot.IsLongTerm(lot, today)
			if ltErr == nil {
				if longTerm {
					l.STCGUSD = "0"
					l.LTCGUSD = l.PnLUSD
				} else {
					l.STCGUSD = l.PnLUSD
					l.LTCGUSD = "0"
				}
			}
		}
		lots = append(lots, l)
	}
	// Sort lots by date, then symbol, then account for chronological display.
	sort.Slice(lots, func(i, j int) bool {
		if lots[i].Date != lots[j].Date {
			return lots[i].Date < lots[j].Date
		}
		if lots[i].Symbol != lots[j].Symbol {
			return lots[i].Symbol < lots[j].Symbol
		}
		return lots[i].Account < lots[j].Account
	})
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
