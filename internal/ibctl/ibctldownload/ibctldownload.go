// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctldownload provides the download orchestrator for IBKR data.
//
// The downloader fetches trade history by making multiple Flex Query API calls
// with sliding 365-day date windows, going backwards from today until a window
// returns zero trades (indicating the beginning of the account has been reached).
// Trades are deduplicated by trade ID across windows.
package ibctldownload

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	datav1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/data/v1"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/bufdev/ibctl/internal/ibctl/ibctltaxlot"
	"github.com/bufdev/ibctl/internal/pkg/frankfurter"
	"github.com/bufdev/ibctl/internal/pkg/ibkrflexquery"
	"github.com/bufdev/ibctl/internal/pkg/moneypb"
	"github.com/bufdev/ibctl/internal/pkg/protoio"
	"github.com/bufdev/ibctl/internal/pkg/timepb"
	"github.com/bufdev/ibctl/internal/standard/xtime"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// windowDays is the number of days per API request (IBKR's maximum).
const windowDays = 365

// Downloader is the interface for downloading and caching IBKR data.
type Downloader interface {
	// Download fetches IBKR data via the Flex Query API and caches it as JSON files.
	// Makes multiple API calls in 365-day windows going backwards until no more
	// trades are found. Always re-downloads fresh data.
	Download(ctx context.Context) error
	// DownloadWindow fetches a single date window and caches the results.
	// Useful for testing whether the IBKR API supports specific historical date ranges.
	DownloadWindow(ctx context.Context, fromDate xtime.Date, toDate xtime.Date) error
	// EnsureDownloaded checks if cached data files exist and downloads if they don't.
	EnsureDownloaded(ctx context.Context) error
}

// NewDownloader creates a new Downloader with all required dependencies.
// The ibkrToken is the Flex Web Service token from the IBKR_TOKEN environment variable.
// The dataDirV1Path is the versioned data directory (e.g., ~/.local/share/ibctl/v1).
func NewDownloader(
	logger *slog.Logger,
	ibkrToken string,
	dataDirV1Path string,
	config *ibctlconfig.Config,
	flexQueryClient ibkrflexquery.Client,
	fxRateClient frankfurter.Client,
) Downloader {
	return &downloader{
		logger:          logger,
		ibkrToken:       ibkrToken,
		dataDirV1Path:   dataDirV1Path,
		config:          config,
		flexQueryClient: flexQueryClient,
		fxRateClient:    fxRateClient,
	}
}

type downloader struct {
	logger          *slog.Logger
	ibkrToken       string
	dataDirV1Path   string
	config          *ibctlconfig.Config
	flexQueryClient ibkrflexquery.Client
	fxRateClient    frankfurter.Client
}

func (d *downloader) EnsureDownloaded(ctx context.Context) error {
	// Check if the minimum required data files exist.
	tradesPath := filepath.Join(d.dataDirV1Path, "trades.json")
	positionsPath := filepath.Join(d.dataDirV1Path, "positions.json")
	if fileExists(tradesPath) && fileExists(positionsPath) {
		return nil
	}
	return d.Download(ctx)
}

func (d *downloader) DownloadWindow(ctx context.Context, fromDate xtime.Date, toDate xtime.Date) error {
	// Create the data directory if needed.
	if err := os.MkdirAll(d.dataDirV1Path, 0o755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}
	// Make a single API call for the specified date range.
	d.logger.Info("downloading single window", "from", fromDate.String(), "to", toDate.String())
	statement, err := d.flexQueryClient.Download(ctx, d.ibkrToken, d.config.IBKRQueryID, fromDate, toDate)
	if err != nil {
		return fmt.Errorf("downloading window (%s to %s): %w", fromDate.String(), toDate.String(), err)
	}
	d.logger.Info("window downloaded",
		"trades", len(statement.Trades),
		"positions", len(statement.OpenPositions),
		"cash_transactions", len(statement.CashTransactions),
	)
	// Convert and write results using the same pipeline as Download.
	return d.processAndWrite(ctx, statement.Trades, statement.OpenPositions, statement.CashTransactions)
}

func (d *downloader) Download(ctx context.Context) error {
	// Create the data directory if needed.
	if err := os.MkdirAll(d.dataDirV1Path, 0o755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}
	d.logger.Info("data directory ready", "path", d.dataDirV1Path)

	// Fetch data across multiple 365-day windows going backwards in time.
	allXMLTrades, xmlPositions, allXMLCashTransactions, err := d.downloadAllWindows(ctx)
	if err != nil {
		return err
	}

	// Process and write all data files.
	return d.processAndWrite(ctx, allXMLTrades, xmlPositions, allXMLCashTransactions)
}

// processAndWrite converts XML data to protos, merges with existing cached data,
// computes tax lots, verifies positions, and writes all JSON data files.
// This is idempotent — running it multiple times with overlapping data produces
// the same result. Trades are deduplicated by trade ID, exchange rates by
// date+currency pair. Positions are always overwritten (they are a point-in-time
// snapshot). Tax lots and metadata are recomputed from the full merged trade set.
func (d *downloader) processAndWrite(
	ctx context.Context,
	xmlTrades []ibkrflexquery.XMLTrade,
	xmlPositions []ibkrflexquery.XMLPosition,
	xmlCashTransactions []ibkrflexquery.XMLCashTransaction,
) error {
	// Convert newly downloaded XML trades to protos.
	newTrades, err := d.convertTrades(xmlTrades)
	if err != nil {
		return err
	}
	// Merge new trades with existing cached trades, deduplicating by trade ID.
	trades := d.mergeTradesWithCache(newTrades)
	tradesPath := filepath.Join(d.dataDirV1Path, "trades.json")
	if err := protoio.WriteMessagesJSON(tradesPath, trades); err != nil {
		return fmt.Errorf("writing trades: %w", err)
	}
	d.logger.Info("trades written", "count", len(trades), "path", tradesPath)

	// Positions are always overwritten with the latest snapshot.
	positions, err := d.convertPositions(xmlPositions)
	if err != nil {
		return err
	}
	positionsPath := filepath.Join(d.dataDirV1Path, "positions.json")
	if err := protoio.WriteMessagesJSON(positionsPath, positions); err != nil {
		return fmt.Errorf("writing positions: %w", err)
	}
	d.logger.Info("positions written", "count", len(positions), "path", positionsPath)

	// Extract new FX rates from cash transactions and merge with cached rates.
	newExchangeRates := d.extractExchangeRates(xmlCashTransactions)
	exchangeRates := d.mergeExchangeRatesWithCache(newExchangeRates)
	// Fetch any rates still missing from frankfurter.dev.
	if err := d.fetchMissingExchangeRates(ctx, exchangeRates, trades); err != nil {
		d.logger.Warn("failed to fetch missing exchange rates", "error", err)
	}
	exchangeRatesPath := filepath.Join(d.dataDirV1Path, "exchange_rates.json")
	if err := protoio.WriteMessagesJSON(exchangeRatesPath, exchangeRates); err != nil {
		return fmt.Errorf("writing exchange rates: %w", err)
	}
	d.logger.Info("exchange rates written", "count", len(exchangeRates), "path", exchangeRatesPath)

	// Compute tax lots from the full merged trade set.
	taxLots, err := ibctltaxlot.ComputeTaxLots(trades)
	if err != nil {
		return fmt.Errorf("computing tax lots: %w", err)
	}
	taxLotsPath := filepath.Join(d.dataDirV1Path, "tax_lots.json")
	if err := protoio.WriteMessagesJSON(taxLotsPath, taxLots); err != nil {
		return fmt.Errorf("writing tax lots: %w", err)
	}
	d.logger.Info("tax lots written", "count", len(taxLots), "path", taxLotsPath)

	// Compute positions from tax lots and verify against IBKR-reported positions.
	computedPositions := ibctltaxlot.ComputePositions(taxLots)
	verificationNotes := ibctltaxlot.VerifyPositions(computedPositions, positions)
	positionsVerified := len(verificationNotes) == 0
	if !positionsVerified {
		for _, note := range verificationNotes {
			d.logger.Warn("position verification", "note", note)
		}
	} else {
		d.logger.Info("all positions verified successfully")
	}

	// Write metadata.json.
	metadata := &datav1.Metadata{
		DownloadTime:      timestamppb.Now(),
		PositionsVerified: positionsVerified,
		VerificationNotes: verificationNotes,
	}
	metadataPath := filepath.Join(d.dataDirV1Path, "metadata.json")
	if err := protoio.WriteMessageJSON(metadataPath, metadata); err != nil {
		return fmt.Errorf("writing metadata: %w", err)
	}
	d.logger.Info("download complete", "positions_verified", positionsVerified)
	return nil
}

// mergeTradesWithCache reads existing cached trades and merges new trades into
// them, deduplicating by trade ID. New trades take precedence over cached trades
// with the same ID.
func (d *downloader) mergeTradesWithCache(newTrades []*datav1.Trade) []*datav1.Trade {
	tradesPath := filepath.Join(d.dataDirV1Path, "trades.json")
	cachedTrades, err := protoio.ReadMessagesJSON(tradesPath, func() *datav1.Trade { return &datav1.Trade{} })
	if err != nil {
		// No cache or read error — start fresh with just the new trades.
		return newTrades
	}
	// Build a map of all trades by trade ID, starting with cached trades.
	tradeMap := make(map[string]*datav1.Trade, len(cachedTrades)+len(newTrades))
	for _, trade := range cachedTrades {
		tradeMap[trade.GetTradeId()] = trade
	}
	// New trades overwrite cached trades with the same ID.
	for _, trade := range newTrades {
		tradeMap[trade.GetTradeId()] = trade
	}
	// Collect and sort for deterministic output.
	merged := make([]*datav1.Trade, 0, len(tradeMap))
	for _, trade := range tradeMap {
		merged = append(merged, trade)
	}
	sort.Slice(merged, func(i, j int) bool {
		dateI := tradeDateString(merged[i])
		dateJ := tradeDateString(merged[j])
		if dateI != dateJ {
			return dateI < dateJ
		}
		return merged[i].GetTradeId() < merged[j].GetTradeId()
	})
	d.logger.Info("merged trades", "cached", len(cachedTrades), "new", len(newTrades), "merged", len(merged))
	return merged
}

// mergeExchangeRatesWithCache reads existing cached exchange rates and merges new
// rates into them, deduplicating by date + base currency + quote currency.
func (d *downloader) mergeExchangeRatesWithCache(newRates []*datav1.ExchangeRate) []*datav1.ExchangeRate {
	exchangeRatesPath := filepath.Join(d.dataDirV1Path, "exchange_rates.json")
	cachedRates, err := protoio.ReadMessagesJSON(exchangeRatesPath, func() *datav1.ExchangeRate { return &datav1.ExchangeRate{} })
	if err != nil {
		// No cache or read error — start fresh with just the new rates.
		return newRates
	}
	// Build a map of all rates by date+currencies, starting with cached rates.
	type rateKey struct {
		date          string
		baseCurrency  string
		quoteCurrency string
	}
	rateMap := make(map[rateKey]*datav1.ExchangeRate, len(cachedRates)+len(newRates))
	for _, rate := range cachedRates {
		key := rateKey{
			date:          exchangeRateDateString(rate),
			baseCurrency:  rate.GetBaseCurrencyCode(),
			quoteCurrency: rate.GetQuoteCurrencyCode(),
		}
		rateMap[key] = rate
	}
	// New rates overwrite cached rates with the same key.
	for _, rate := range newRates {
		key := rateKey{
			date:          exchangeRateDateString(rate),
			baseCurrency:  rate.GetBaseCurrencyCode(),
			quoteCurrency: rate.GetQuoteCurrencyCode(),
		}
		rateMap[key] = rate
	}
	// Collect and sort for deterministic output.
	merged := make([]*datav1.ExchangeRate, 0, len(rateMap))
	for _, rate := range rateMap {
		merged = append(merged, rate)
	}
	sort.Slice(merged, func(i, j int) bool {
		return exchangeRateDateString(merged[i]) < exchangeRateDateString(merged[j])
	})
	d.logger.Info("merged exchange rates", "cached", len(cachedRates), "new", len(newRates), "merged", len(merged))
	return merged
}

// tradeDateString returns a sortable date string from a trade's trade_date.
func tradeDateString(trade *datav1.Trade) string {
	if d := trade.GetTradeDate(); d != nil {
		return fmt.Sprintf("%04d-%02d-%02d", d.GetYear(), d.GetMonth(), d.GetDay())
	}
	return ""
}

// exchangeRateDateString returns a sortable date string from an exchange rate's date.
func exchangeRateDateString(rate *datav1.ExchangeRate) string {
	if d := rate.GetDate(); d != nil {
		return fmt.Sprintf("%04d-%02d-%02d", d.GetYear(), d.GetMonth(), d.GetDay())
	}
	return ""
}

// downloadAllWindows fetches data across multiple 365-day windows going backwards
// in time. Trades and cash transactions are accumulated and deduplicated across
// all windows. Positions are taken from the most recent window only (they are a
// point-in-time snapshot). The loop terminates when a window returns zero trades,
// indicating the beginning of the account has been reached.
func (d *downloader) downloadAllWindows(ctx context.Context) (
	[]ibkrflexquery.XMLTrade,
	[]ibkrflexquery.XMLPosition,
	[]ibkrflexquery.XMLCashTransaction,
	error,
) {
	// Deduplicate trades by trade ID across windows.
	seenTradeIDs := make(map[string]bool)
	var allTrades []ibkrflexquery.XMLTrade
	var allCashTransactions []ibkrflexquery.XMLCashTransaction
	// Positions from the most recent window only (point-in-time snapshot).
	var positions []ibkrflexquery.XMLPosition

	today := xtime.TimeToDate(time.Now())
	for window := range 100 { // Safety limit to prevent infinite loops.
		// Compute the date range for this window using xtime.Date arithmetic.
		toDate := today.AddDays(-window * windowDays)
		fromDate := toDate.AddDays(-(windowDays - 1))

		d.logger.Info("downloading window", "window", window, "from", fromDate.String(), "to", toDate.String())
		statement, err := d.flexQueryClient.Download(ctx, d.ibkrToken, d.config.IBKRQueryID, fromDate, toDate)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("downloading window %d (%s to %s): %w", window, fromDate.String(), toDate.String(), err)
		}

		// Positions: take from the most recent window only (window 0).
		if window == 0 {
			positions = statement.OpenPositions
		}

		// Accumulate trades, deduplicating by trade ID.
		newTradeCount := 0
		for i := range statement.Trades {
			tradeID := statement.Trades[i].TradeID
			if seenTradeIDs[tradeID] {
				continue
			}
			seenTradeIDs[tradeID] = true
			allTrades = append(allTrades, statement.Trades[i])
			newTradeCount++
		}

		// Accumulate cash transactions across all windows.
		allCashTransactions = append(allCashTransactions, statement.CashTransactions...)

		d.logger.Info("window downloaded", "window", window, "new_trades", newTradeCount, "total_trades", len(allTrades))

		// Termination: stop when a window returns zero trades (reached beginning of account).
		if len(statement.Trades) == 0 {
			d.logger.Info("no trades in window, stopping", "window", window)
			break
		}
	}

	return allTrades, positions, allCashTransactions, nil
}

// convertTrades converts XML trades to proto trades.
func (d *downloader) convertTrades(xmlTrades []ibkrflexquery.XMLTrade) ([]*datav1.Trade, error) {
	trades := make([]*datav1.Trade, 0, len(xmlTrades))
	for i := range xmlTrades {
		trade, err := xmlTradeToProto(&xmlTrades[i])
		if err != nil {
			return nil, fmt.Errorf("converting trade %d: %w", i, err)
		}
		trades = append(trades, trade)
	}
	return trades, nil
}

// convertPositions converts XML positions to proto positions.
func (d *downloader) convertPositions(xmlPositions []ibkrflexquery.XMLPosition) ([]*datav1.Position, error) {
	positions := make([]*datav1.Position, 0, len(xmlPositions))
	for i := range xmlPositions {
		position, err := xmlPositionToProto(&xmlPositions[i])
		if err != nil {
			return nil, fmt.Errorf("converting position %d: %w", i, err)
		}
		positions = append(positions, position)
	}
	return positions, nil
}

// extractExchangeRates extracts FX rates from cash transactions.
func (d *downloader) extractExchangeRates(cashTransactions []ibkrflexquery.XMLCashTransaction) []*datav1.ExchangeRate {
	// Use a set to avoid duplicate rates for the same date/currency pair.
	type rateKey struct {
		date          string
		baseCurrency  string
		quoteCurrency string
	}
	seen := make(map[rateKey]bool)
	var rates []*datav1.ExchangeRate
	for _, ct := range cashTransactions {
		if ct.FxRateToBase == "" || ct.FxRateToBase == "1" {
			continue
		}
		// Parse the date from the dateTime field (format: YYYYMMDD or YYYYMMDD;HHMMSS).
		dateStr := ct.DateTime
		if len(dateStr) >= 8 {
			dateStr = dateStr[:8]
		}
		parsedDate, err := parseIBKRDate(dateStr)
		if err != nil {
			continue
		}
		key := rateKey{
			date:          dateStr,
			baseCurrency:  ct.Currency,
			quoteCurrency: "USD", // IBKR fxRateToBase is always to the base currency (USD).
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		protoDate, err := timepb.NewProtoDate(parsedDate.Year(), parsedDate.Month(), parsedDate.Day())
		if err != nil {
			continue
		}
		// Parse the FX rate string into units and micros using a dummy Money.
		rateMoney, err := moneypb.NewProtoMoney("USD", ct.FxRateToBase)
		if err != nil {
			continue
		}
		rates = append(rates, &datav1.ExchangeRate{
			Date:              protoDate,
			BaseCurrencyCode:  ct.Currency,
			QuoteCurrencyCode: "USD",
			RateUnits:         rateMoney.GetUnits(),
			RateMicros:        rateMoney.GetMicros(),
			Provider:          "ibkr",
		})
	}
	// Sort by date for deterministic output.
	sort.Slice(rates, func(i, j int) bool {
		dateI := fmt.Sprintf("%04d-%02d-%02d", rates[i].GetDate().GetYear(), rates[i].GetDate().GetMonth(), rates[i].GetDate().GetDay())
		dateJ := fmt.Sprintf("%04d-%02d-%02d", rates[j].GetDate().GetYear(), rates[j].GetDate().GetMonth(), rates[j].GetDate().GetDay())
		return dateI < dateJ
	})
	return rates
}

// fetchMissingExchangeRates uses the frankfurter.dev API as a fallback for any dates
// that have trades in non-USD currencies but no FX rate from IBKR.
func (d *downloader) fetchMissingExchangeRates(ctx context.Context, existingRates []*datav1.ExchangeRate, trades []*datav1.Trade) error {
	// Build set of existing rate dates.
	type rateKey struct {
		date     string
		currency string
	}
	existingSet := make(map[rateKey]bool)
	for _, rate := range existingRates {
		dateStr := fmt.Sprintf("%04d-%02d-%02d", rate.GetDate().GetYear(), rate.GetDate().GetMonth(), rate.GetDate().GetDay())
		existingSet[rateKey{date: dateStr, currency: rate.GetBaseCurrencyCode()}] = true
	}
	// Find trade dates with non-USD currencies that don't have FX rates.
	missingDates := make(map[string]map[string]bool) // currency -> set of dates
	for _, trade := range trades {
		if trade.GetCurrencyCode() == "USD" {
			continue
		}
		dateStr := fmt.Sprintf("%04d-%02d-%02d", trade.GetTradeDate().GetYear(), trade.GetTradeDate().GetMonth(), trade.GetTradeDate().GetDay())
		key := rateKey{date: dateStr, currency: trade.GetCurrencyCode()}
		if existingSet[key] {
			continue
		}
		if missingDates[trade.GetCurrencyCode()] == nil {
			missingDates[trade.GetCurrencyCode()] = make(map[string]bool)
		}
		missingDates[trade.GetCurrencyCode()][dateStr] = true
	}
	if len(missingDates) == 0 {
		return nil
	}
	d.logger.Info("fetching missing exchange rates from frankfurter.dev")
	// Fetch missing rates for each currency.
	for currency, dates := range missingDates {
		// Find the date range.
		var sortedDates []string
		for date := range dates {
			sortedDates = append(sortedDates, date)
		}
		sort.Strings(sortedDates)
		startDate := sortedDates[0]
		endDate := sortedDates[len(sortedDates)-1]
		// Fetch rates from the FX rate client.
		rates, err := d.fxRateClient.GetRates(ctx, currency, "USD", startDate, endDate)
		if err != nil {
			return fmt.Errorf("fetching rates for %s: %w", currency, err)
		}
		// Add fetched rates to the existing rates.
		for dateStr, rate := range rates {
			if !dates[dateStr] {
				continue
			}
			parsedDate, err := time.Parse("2006-01-02", dateStr)
			if err != nil {
				continue
			}
			protoDate, err := timepb.NewProtoDate(parsedDate.Year(), parsedDate.Month(), parsedDate.Day())
			if err != nil {
				continue
			}
			// Parse the FX rate string into units and micros using a dummy Money.
			rateMoney, err := moneypb.NewProtoMoney("USD", rate)
			if err != nil {
				continue
			}
			existingRates = append(existingRates, &datav1.ExchangeRate{
				Date:              protoDate,
				BaseCurrencyCode:  currency,
				QuoteCurrencyCode: "USD",
				RateUnits:         rateMoney.GetUnits(),
				RateMicros:        rateMoney.GetMicros(),
				Provider:          "frankfurter",
			})
		}
	}
	return nil
}

// xmlTradeToProto converts an XML trade from the Flex Query response to a proto Trade.
func xmlTradeToProto(xmlTrade *ibkrflexquery.XMLTrade) (*datav1.Trade, error) {
	// Parse the trade date (format: YYYYMMDD).
	tradeDate, err := parseIBKRDate(xmlTrade.TradeDate)
	if err != nil {
		return nil, fmt.Errorf("parsing trade date %q: %w", xmlTrade.TradeDate, err)
	}
	protoTradeDate, err := timepb.NewProtoDate(tradeDate.Year(), tradeDate.Month(), tradeDate.Day())
	if err != nil {
		return nil, err
	}
	// Parse the settle date (format: YYYYMMDD).
	settleDate, err := parseIBKRDate(xmlTrade.SettleDateTarget)
	if err != nil {
		return nil, fmt.Errorf("parsing settle date %q: %w", xmlTrade.SettleDateTarget, err)
	}
	protoSettleDate, err := timepb.NewProtoDate(settleDate.Year(), settleDate.Month(), settleDate.Day())
	if err != nil {
		return nil, err
	}
	// Parse numeric fields using the Money proto helper.
	currencyCode := xmlTrade.Currency
	tradePrice, err := moneypb.NewProtoMoney(currencyCode, xmlTrade.TradePrice)
	if err != nil {
		return nil, fmt.Errorf("parsing trade price: %w", err)
	}
	proceeds, err := moneypb.NewProtoMoney(currencyCode, xmlTrade.Proceeds)
	if err != nil {
		return nil, fmt.Errorf("parsing proceeds: %w", err)
	}
	commission, err := moneypb.NewProtoMoney(currencyCode, xmlTrade.IBCommission)
	if err != nil {
		return nil, fmt.Errorf("parsing commission: %w", err)
	}
	fifoPnlRealized, err := moneypb.NewProtoMoney(currencyCode, xmlTrade.FifoPnlRealized)
	if err != nil {
		return nil, fmt.Errorf("parsing fifo pnl realized: %w", err)
	}
	// Parse the quantity as an integer.
	quantity, err := strconv.ParseInt(xmlTrade.Quantity, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing quantity %q: %w", xmlTrade.Quantity, err)
	}
	return &datav1.Trade{
		TradeId:         xmlTrade.TradeID,
		TradeDate:       protoTradeDate,
		SettleDate:      protoSettleDate,
		Symbol:          xmlTrade.Symbol,
		Description:     xmlTrade.Description,
		AssetCategory:   xmlTrade.AssetCategory,
		Side:            parseTradeSide(xmlTrade.BuySell),
		Quantity:        quantity,
		TradePrice:      tradePrice,
		Proceeds:        proceeds,
		Commission:      commission,
		CurrencyCode:    currencyCode,
		FifoPnlRealized: fifoPnlRealized,
	}, nil
}

// xmlPositionToProto converts an XML position from the Flex Query response to a proto Position.
func xmlPositionToProto(xmlPosition *ibkrflexquery.XMLPosition) (*datav1.Position, error) {
	currencyCode := xmlPosition.Currency
	// Parse numeric fields using the Money proto helper.
	costBasisPrice, err := moneypb.NewProtoMoney(currencyCode, xmlPosition.CostBasisPrice)
	if err != nil {
		return nil, fmt.Errorf("parsing cost basis price: %w", err)
	}
	marketPrice, err := moneypb.NewProtoMoney(currencyCode, xmlPosition.MarkPrice)
	if err != nil {
		return nil, fmt.Errorf("parsing market price: %w", err)
	}
	marketValue, err := moneypb.NewProtoMoney(currencyCode, xmlPosition.PositionValue)
	if err != nil {
		return nil, fmt.Errorf("parsing market value: %w", err)
	}
	fifoPnlUnrealized, err := moneypb.NewProtoMoney(currencyCode, xmlPosition.FifoPnlUnrealized)
	if err != nil {
		return nil, fmt.Errorf("parsing fifo pnl unrealized: %w", err)
	}
	// Parse the quantity as an integer.
	quantity, err := strconv.ParseInt(xmlPosition.Quantity, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing quantity %q: %w", xmlPosition.Quantity, err)
	}
	return &datav1.Position{
		Symbol:            xmlPosition.Symbol,
		Description:       xmlPosition.Description,
		AssetCategory:     xmlPosition.AssetCategory,
		Quantity:          quantity,
		CostBasisPrice:    costBasisPrice,
		MarketPrice:       marketPrice,
		MarketValue:       marketValue,
		FifoPnlUnrealized: fifoPnlUnrealized,
		CurrencyCode:      currencyCode,
	}, nil
}

// parseTradeSide converts an IBKR buy/sell string to a TradeSide enum value.
func parseTradeSide(s string) datav1.TradeSide {
	switch s {
	case "BUY":
		return datav1.TradeSide_TRADE_SIDE_BUY
	case "SELL":
		return datav1.TradeSide_TRADE_SIDE_SELL
	default:
		return datav1.TradeSide_TRADE_SIDE_UNSPECIFIED
	}
}

// parseIBKRDate parses an IBKR date string in YYYYMMDD format.
func parseIBKRDate(s string) (time.Time, error) {
	return time.Parse("20060102", s)
}

// fileExists checks if a file exists at the given path.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
