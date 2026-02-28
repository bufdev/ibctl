// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctldownload provides the download orchestrator for IBKR data.
//
// The downloader fetches data via the IBKR Flex Query API and stores it per
// account under v1/<alias>/. Trades are deduplicated by trade ID, exchange
// rates by date+currency pair. Downloads are idempotent — running multiple
// times safely merges new data into the cache.
package ibctldownload

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	datav1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/data/v1"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/bufdev/ibctl/internal/pkg/frankfurter"
	"github.com/bufdev/ibctl/internal/pkg/ibkrflexquery"
	"github.com/bufdev/ibctl/internal/pkg/mathpb"
	"github.com/bufdev/ibctl/internal/pkg/moneypb"
	"github.com/bufdev/ibctl/internal/pkg/protoio"
	"github.com/bufdev/ibctl/internal/pkg/timepb"
	"github.com/bufdev/ibctl/internal/standard/xtime"
)

// Downloader is the interface for downloading and caching IBKR data.
type Downloader interface {
	// Download fetches IBKR data via the Flex Query API and merges it with
	// cached data. Data is stored per account under v1/<alias>/.
	// Idempotent — safe to call multiple times.
	Download(ctx context.Context) error
	// EnsureDownloaded checks if cached data files exist and downloads if they don't.
	EnsureDownloaded(ctx context.Context) error
}

// NewDownloader creates a new Downloader with all required dependencies.
// The ibkrToken is the Flex Web Service token from the IBKR_FLEX_WEB_SERVICE_TOKEN environment variable.
// The dataDirV1Path is the versioned data directory (e.g., <data_dir>/v1).
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
	// Check if at least one account has cached data.
	for alias := range d.config.AccountAliases {
		accountDir := filepath.Join(d.dataDirV1Path, alias)
		tradesPath := filepath.Join(accountDir, "trades.json")
		positionsPath := filepath.Join(accountDir, "positions.json")
		if fileExists(tradesPath) && fileExists(positionsPath) {
			// At least one account has data — assume we're good.
			return nil
		}
	}
	return d.Download(ctx)
}

func (d *downloader) Download(ctx context.Context) error {
	// Create the data directory if needed.
	if err := os.MkdirAll(d.dataDirV1Path, 0o755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}
	// Warn if old flat data files exist (pre-multi-account format).
	oldTradesPath := filepath.Join(d.dataDirV1Path, "trades.json")
	if fileExists(oldTradesPath) {
		d.logger.Warn("found old flat data files, these will be ignored in favor of per-account data",
			"path", oldTradesPath,
		)
	}
	d.logger.Info("downloading flex query data")
	// Fetch data using the query's configured period (single API call).
	var zeroDate xtime.Date
	statements, err := d.flexQueryClient.Download(ctx, d.ibkrToken, d.config.IBKRFlexQueryID, zeroDate, zeroDate)
	if err != nil {
		return fmt.Errorf("downloading flex query: %w", err)
	}
	d.logger.Info("flex query data downloaded", "accounts", len(statements))
	// Collect all trades across accounts for FX rate gap detection.
	var allTrades []*datav1.Trade
	// Process each account's statement.
	for _, statement := range statements {
		// Look up the account alias from the IBKR account ID.
		alias, ok := d.config.AccountIDToAlias[statement.AccountId]
		if !ok {
			d.logger.Warn("unknown account ID, skipping (add it to the accounts section in config)",
				"account_id", statement.AccountId,
			)
			continue
		}
		// Create the per-account directory.
		accountDir := filepath.Join(d.dataDirV1Path, alias)
		if err := os.MkdirAll(accountDir, 0o755); err != nil {
			return fmt.Errorf("creating account directory for %s: %w", alias, err)
		}
		// Process and write account-specific data.
		trades, err := d.processAccountData(alias, accountDir, &statement)
		if err != nil {
			return fmt.Errorf("processing account %s: %w", alias, err)
		}
		allTrades = append(allTrades, trades...)
	}
	// Exchange rates are shared across accounts — merge from all cash transactions.
	var allCashTransactions []ibkrflexquery.XMLCashTransaction
	for _, statement := range statements {
		allCashTransactions = append(allCashTransactions, statement.CashTransactions...)
	}
	newExchangeRates := d.extractExchangeRates(allCashTransactions)
	exchangeRates := d.mergeExchangeRatesWithCache(newExchangeRates)
	// Fetch any rates still missing from frankfurter.dev.
	if err := d.fetchMissingExchangeRates(ctx, exchangeRates, allTrades); err != nil {
		d.logger.Warn("failed to fetch missing exchange rates", "error", err)
	}
	exchangeRatesPath := filepath.Join(d.dataDirV1Path, "exchange_rates.json")
	if err := protoio.WriteMessagesJSON(exchangeRatesPath, exchangeRates); err != nil {
		return fmt.Errorf("writing exchange rates: %w", err)
	}
	d.logger.Info("exchange rates written", "count", len(exchangeRates), "path", exchangeRatesPath)
	d.logger.Info("download complete")
	return nil
}

// processAccountData converts XML data to protos, merges with existing cache,
// and writes per-account data files. Returns the merged trades for FX rate processing.
func (d *downloader) processAccountData(alias string, accountDir string, statement *ibkrflexquery.FlexStatement) ([]*datav1.Trade, error) {
	// Convert and merge trades.
	newTrades, err := d.convertTrades(statement.Trades, alias)
	if err != nil {
		return nil, err
	}
	trades := d.mergeTradesWithCache(newTrades, accountDir)
	tradesPath := filepath.Join(accountDir, "trades.json")
	if err := protoio.WriteMessagesJSON(tradesPath, trades); err != nil {
		return nil, fmt.Errorf("writing trades: %w", err)
	}
	// Positions are always overwritten with the latest snapshot.
	positions, err := d.convertPositions(statement.OpenPositions, alias)
	if err != nil {
		return nil, err
	}
	positionsPath := filepath.Join(accountDir, "positions.json")
	if err := protoio.WriteMessagesJSON(positionsPath, positions); err != nil {
		return nil, fmt.Errorf("writing positions: %w", err)
	}
	// Convert and write transfers.
	transfers, err := d.convertTransfers(statement.Transfers, alias)
	if err != nil {
		return nil, err
	}
	transfersPath := filepath.Join(accountDir, "transfers.json")
	if err := protoio.WriteMessagesJSON(transfersPath, transfers); err != nil {
		return nil, fmt.Errorf("writing transfers: %w", err)
	}
	// Convert and write trade transfers.
	tradeTransfers, err := d.convertTradeTransfers(statement.TradeTransfers, alias)
	if err != nil {
		return nil, err
	}
	tradeTransfersPath := filepath.Join(accountDir, "trade_transfers.json")
	if err := protoio.WriteMessagesJSON(tradeTransfersPath, tradeTransfers); err != nil {
		return nil, fmt.Errorf("writing trade transfers: %w", err)
	}
	// Convert and write corporate actions.
	corporateActions, err := d.convertCorporateActions(statement.CorporateActions, alias)
	if err != nil {
		return nil, err
	}
	corporateActionsPath := filepath.Join(accountDir, "corporate_actions.json")
	if err := protoio.WriteMessagesJSON(corporateActionsPath, corporateActions); err != nil {
		return nil, fmt.Errorf("writing corporate actions: %w", err)
	}
	d.logger.Info("account data written",
		"account", alias,
		"trades", len(trades),
		"positions", len(positions),
		"transfers", len(transfers),
		"trade_transfers", len(tradeTransfers),
		"corporate_actions", len(corporateActions),
	)
	return trades, nil
}

// mergeTradesWithCache reads existing cached trades from the account directory
// and merges new trades, deduplicating by trade ID.
func (d *downloader) mergeTradesWithCache(newTrades []*datav1.Trade, accountDir string) []*datav1.Trade {
	tradesPath := filepath.Join(accountDir, "trades.json")
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
	// Exchange rates are global (not per-account).
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

// convertTrades converts XML trades to proto trades, setting the account alias.
func (d *downloader) convertTrades(xmlTrades []ibkrflexquery.XMLTrade, accountAlias string) ([]*datav1.Trade, error) {
	trades := make([]*datav1.Trade, 0, len(xmlTrades))
	for i := range xmlTrades {
		trade, err := xmlTradeToProto(&xmlTrades[i], accountAlias)
		if err != nil {
			return nil, fmt.Errorf("converting trade %d: %w", i, err)
		}
		trades = append(trades, trade)
	}
	return trades, nil
}

// convertPositions converts XML positions to proto positions, setting the account alias.
func (d *downloader) convertPositions(xmlPositions []ibkrflexquery.XMLPosition, accountAlias string) ([]*datav1.Position, error) {
	positions := make([]*datav1.Position, 0, len(xmlPositions))
	for i := range xmlPositions {
		position, err := xmlPositionToProto(&xmlPositions[i], accountAlias)
		if err != nil {
			return nil, fmt.Errorf("converting position %d: %w", i, err)
		}
		positions = append(positions, position)
	}
	return positions, nil
}

// convertTransfers converts XML transfers to proto transfers, setting the account alias.
func (d *downloader) convertTransfers(xmlTransfers []ibkrflexquery.XMLTransfer, accountAlias string) ([]*datav1.Transfer, error) {
	transfers := make([]*datav1.Transfer, 0, len(xmlTransfers))
	for i := range xmlTransfers {
		transfer, err := xmlTransferToProto(&xmlTransfers[i], accountAlias)
		if err != nil {
			d.logger.Warn("skipping unparseable transfer", "index", i, "error", err)
			continue
		}
		transfers = append(transfers, transfer)
	}
	return transfers, nil
}

// convertTradeTransfers converts XML trade transfers to proto trade transfers.
func (d *downloader) convertTradeTransfers(xmlTradeTransfers []ibkrflexquery.XMLTradeTransfer, accountAlias string) ([]*datav1.TradeTransfer, error) {
	tradeTransfers := make([]*datav1.TradeTransfer, 0, len(xmlTradeTransfers))
	for i := range xmlTradeTransfers {
		tt, err := xmlTradeTransferToProto(&xmlTradeTransfers[i], accountAlias)
		if err != nil {
			d.logger.Warn("skipping unparseable trade transfer", "index", i, "error", err)
			continue
		}
		tradeTransfers = append(tradeTransfers, tt)
	}
	return tradeTransfers, nil
}

// convertCorporateActions converts XML corporate actions to proto corporate actions.
func (d *downloader) convertCorporateActions(xmlActions []ibkrflexquery.XMLCorporateAction, accountAlias string) ([]*datav1.CorporateAction, error) {
	actions := make([]*datav1.CorporateAction, 0, len(xmlActions))
	for i := range xmlActions {
		action, err := xmlCorporateActionToProto(&xmlActions[i], accountAlias)
		if err != nil {
			d.logger.Warn("skipping unparseable corporate action", "index", i, "error", err)
			continue
		}
		actions = append(actions, action)
	}
	return actions, nil
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
		// Parse the FX rate string into a Decimal.
		rate, err := mathpb.NewDecimal(ct.FxRateToBase)
		if err != nil {
			continue
		}
		rates = append(rates, &datav1.ExchangeRate{
			Date:              protoDate,
			BaseCurrencyCode:  ct.Currency,
			QuoteCurrencyCode: "USD",
			Rate:              rate,
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
			// Parse the FX rate string into a Decimal.
			rateDecimal, err := mathpb.NewDecimal(rate)
			if err != nil {
				continue
			}
			existingRates = append(existingRates, &datav1.ExchangeRate{
				Date:              protoDate,
				BaseCurrencyCode:  currency,
				QuoteCurrencyCode: "USD",
				Rate:              rateDecimal,
				Provider:          "frankfurter",
			})
		}
	}
	return nil
}

// xmlTradeToProto converts an XML trade from the Flex Query response to a proto Trade.
func xmlTradeToProto(xmlTrade *ibkrflexquery.XMLTrade, accountAlias string) (*datav1.Trade, error) {
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
	// Parse the quantity as a decimal (supports fractional shares).
	quantity, err := mathpb.NewDecimal(xmlTrade.Quantity)
	if err != nil {
		return nil, fmt.Errorf("parsing quantity %q: %w", xmlTrade.Quantity, err)
	}
	return &datav1.Trade{
		TradeId:         xmlTrade.TradeID,
		AccountId:       accountAlias,
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
func xmlPositionToProto(xmlPosition *ibkrflexquery.XMLPosition, accountAlias string) (*datav1.Position, error) {
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
	// Parse the quantity as a decimal (supports fractional shares).
	quantity, err := mathpb.NewDecimal(xmlPosition.Quantity)
	if err != nil {
		return nil, fmt.Errorf("parsing quantity %q: %w", xmlPosition.Quantity, err)
	}
	return &datav1.Position{
		Symbol:            xmlPosition.Symbol,
		AccountId:         accountAlias,
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

// xmlTransferToProto converts an XML transfer to a proto Transfer.
func xmlTransferToProto(xmlTransfer *ibkrflexquery.XMLTransfer, accountAlias string) (*datav1.Transfer, error) {
	// Parse the date from the dateTime field (format: YYYYMMDD or YYYYMMDD;HHMMSS).
	dateStr := xmlTransfer.DateTime
	if len(dateStr) >= 8 {
		dateStr = dateStr[:8]
	}
	parsedDate, err := parseIBKRDate(dateStr)
	if err != nil {
		return nil, fmt.Errorf("parsing transfer date %q: %w", xmlTransfer.DateTime, err)
	}
	protoDate, err := timepb.NewProtoDate(parsedDate.Year(), parsedDate.Month(), parsedDate.Day())
	if err != nil {
		return nil, err
	}
	// Parse the quantity as a decimal.
	quantity, err := mathpb.NewDecimal(xmlTransfer.Quantity)
	if err != nil {
		return nil, fmt.Errorf("parsing transfer quantity %q: %w", xmlTransfer.Quantity, err)
	}
	// Parse the transfer price if available.
	var transferPrice *datav1.Transfer
	_ = transferPrice // Avoid unused variable.
	currencyCode := xmlTransfer.Currency
	transfer := &datav1.Transfer{
		AccountId:     accountAlias,
		Type:          parseTransferType(xmlTransfer.Type),
		Direction:     parseTransferDirection(xmlTransfer.Direction),
		Symbol:        xmlTransfer.Symbol,
		Date:          protoDate,
		Quantity:      quantity,
		CurrencyCode:  currencyCode,
		Description:   xmlTransfer.Description,
		AssetCategory: xmlTransfer.AssetCategory,
	}
	// Parse transfer price if available.
	if xmlTransfer.TransferPrice != "" && xmlTransfer.TransferPrice != "0" {
		price, err := moneypb.NewProtoMoney(currencyCode, xmlTransfer.TransferPrice)
		if err == nil {
			transfer.TransferPrice = price
		}
	}
	return transfer, nil
}

// xmlTradeTransferToProto converts an XML trade transfer to a proto TradeTransfer.
func xmlTradeTransferToProto(xmlTT *ibkrflexquery.XMLTradeTransfer, accountAlias string) (*datav1.TradeTransfer, error) {
	// Parse the date from the dateTime field.
	dateStr := xmlTT.DateTime
	if len(dateStr) >= 8 {
		dateStr = dateStr[:8]
	}
	parsedDate, err := parseIBKRDate(dateStr)
	if err != nil {
		return nil, fmt.Errorf("parsing trade transfer date %q: %w", xmlTT.DateTime, err)
	}
	protoDate, err := timepb.NewProtoDate(parsedDate.Year(), parsedDate.Month(), parsedDate.Day())
	if err != nil {
		return nil, err
	}
	// Parse the quantity as a decimal.
	quantity, err := mathpb.NewDecimal(xmlTT.Quantity)
	if err != nil {
		return nil, fmt.Errorf("parsing trade transfer quantity %q: %w", xmlTT.Quantity, err)
	}
	currencyCode := xmlTT.Currency
	tt := &datav1.TradeTransfer{
		AccountId:     accountAlias,
		Symbol:        xmlTT.Symbol,
		Date:          protoDate,
		Quantity:      quantity,
		OrigTradeId:   xmlTT.OrigTradeID,
		CurrencyCode:  currencyCode,
		AssetCategory: xmlTT.AssetCategory,
	}
	// Parse original trade price if available.
	if xmlTT.OrigTradePrice != "" {
		origPrice, err := moneypb.NewProtoMoney(currencyCode, xmlTT.OrigTradePrice)
		if err == nil {
			tt.OrigTradePrice = origPrice
		}
	}
	// Parse original trade date if available (preserves holding period).
	if xmlTT.OrigTradeDate != "" {
		origDate, err := parseIBKRDate(xmlTT.OrigTradeDate)
		if err == nil {
			protoOrigDate, err := timepb.NewProtoDate(origDate.Year(), origDate.Month(), origDate.Day())
			if err == nil {
				tt.OrigTradeDate = protoOrigDate
			}
		}
	}
	// Parse cost basis if available.
	if xmlTT.Cost != "" {
		cost, err := moneypb.NewProtoMoney(currencyCode, xmlTT.Cost)
		if err == nil {
			tt.Cost = cost
		}
	}
	// Parse holding period date if available.
	if xmlTT.HoldingPeriodDateTime != "" {
		hpDateStr := xmlTT.HoldingPeriodDateTime
		if len(hpDateStr) >= 8 {
			hpDateStr = hpDateStr[:8]
		}
		hpDate, err := parseIBKRDate(hpDateStr)
		if err == nil {
			protoHPDate, err := timepb.NewProtoDate(hpDate.Year(), hpDate.Month(), hpDate.Day())
			if err == nil {
				tt.HoldingPeriodDate = protoHPDate
			}
		}
	}
	return tt, nil
}

// xmlCorporateActionToProto converts an XML corporate action to a proto CorporateAction.
func xmlCorporateActionToProto(xmlAction *ibkrflexquery.XMLCorporateAction, accountAlias string) (*datav1.CorporateAction, error) {
	// Parse the date from the dateTime field.
	dateStr := xmlAction.DateTime
	if len(dateStr) >= 8 {
		dateStr = dateStr[:8]
	}
	parsedDate, err := parseIBKRDate(dateStr)
	if err != nil {
		return nil, fmt.Errorf("parsing corporate action date %q: %w", xmlAction.DateTime, err)
	}
	protoDate, err := timepb.NewProtoDate(parsedDate.Year(), parsedDate.Month(), parsedDate.Day())
	if err != nil {
		return nil, err
	}
	// Parse the quantity as a decimal.
	quantity, err := mathpb.NewDecimal(xmlAction.Quantity)
	if err != nil {
		return nil, fmt.Errorf("parsing corporate action quantity %q: %w", xmlAction.Quantity, err)
	}
	currencyCode := xmlAction.Currency
	action := &datav1.CorporateAction{
		AccountId:         accountAlias,
		Type:              parseCorporateActionType(xmlAction.Type),
		Date:              protoDate,
		Symbol:            xmlAction.Symbol,
		Quantity:          quantity,
		CurrencyCode:      currencyCode,
		ActionDescription: xmlAction.ActionDescription,
		AssetCategory:     xmlAction.AssetCategory,
	}
	// Parse amount if available.
	if xmlAction.Amount != "" && xmlAction.Amount != "0" {
		amount, err := moneypb.NewProtoMoney(currencyCode, xmlAction.Amount)
		if err == nil {
			action.Amount = amount
		}
	}
	return action, nil
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

// parseTransferType converts an IBKR transfer type string to a TransferType enum value.
func parseTransferType(s string) datav1.TransferType {
	switch s {
	case "ACATS":
		return datav1.TransferType_TRANSFER_TYPE_ACATS
	case "ATON":
		return datav1.TransferType_TRANSFER_TYPE_ATON
	case "FOP":
		return datav1.TransferType_TRANSFER_TYPE_FOP
	case "INTERNAL":
		return datav1.TransferType_TRANSFER_TYPE_INTERNAL
	default:
		return datav1.TransferType_TRANSFER_TYPE_UNSPECIFIED
	}
}

// parseTransferDirection converts an IBKR transfer direction string to a TransferDirection enum value.
func parseTransferDirection(s string) datav1.TransferDirection {
	switch s {
	case "IN":
		return datav1.TransferDirection_TRANSFER_DIRECTION_IN
	case "OUT":
		return datav1.TransferDirection_TRANSFER_DIRECTION_OUT
	default:
		return datav1.TransferDirection_TRANSFER_DIRECTION_UNSPECIFIED
	}
}

// parseCorporateActionType converts an IBKR corporate action type code to a CorporateActionType enum value.
// IBKR uses two-letter codes: FS=Forward Split, RS=Reverse Split, TC=Merger/Tender, SO=Spinoff.
func parseCorporateActionType(s string) datav1.CorporateActionType {
	switch s {
	case "FS":
		return datav1.CorporateActionType_CORPORATE_ACTION_TYPE_FORWARD_SPLIT
	case "RS":
		return datav1.CorporateActionType_CORPORATE_ACTION_TYPE_REVERSE_SPLIT
	case "TC":
		return datav1.CorporateActionType_CORPORATE_ACTION_TYPE_MERGER
	case "SO":
		return datav1.CorporateActionType_CORPORATE_ACTION_TYPE_SPINOFF
	default:
		return datav1.CorporateActionType_CORPORATE_ACTION_TYPE_UNSPECIFIED
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
