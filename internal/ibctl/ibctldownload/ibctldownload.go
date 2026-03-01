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
	"github.com/bufdev/ibctl/internal/ibctl/ibctlpath"
	"github.com/bufdev/ibctl/internal/pkg/bankofcanada"
	"github.com/bufdev/ibctl/internal/pkg/frankfurter"
	"github.com/bufdev/ibctl/internal/pkg/ibkractivitycsv"
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
}

// NewDownloader creates a new Downloader with all required dependencies.
// The ibkrToken is the Flex Web Service token from the IBKR_FLEX_WEB_SERVICE_TOKEN environment variable.
func NewDownloader(
	logger *slog.Logger,
	ibkrToken string,
	config *ibctlconfig.Config,
	flexQueryClient ibkrflexquery.Client,
	fxRateClient frankfurter.Client,
	bocClient bankofcanada.Client,
) Downloader {
	return &downloader{
		logger:          logger,
		ibkrToken:       ibkrToken,
		config:          config,
		flexQueryClient: flexQueryClient,
		fxRateClient:    fxRateClient,
		bocClient:       bocClient,
	}
}

type downloader struct {
	logger          *slog.Logger
	ibkrToken       string
	config          *ibctlconfig.Config
	flexQueryClient ibkrflexquery.Client
	fxRateClient    frankfurter.Client
	bocClient       bankofcanada.Client
}

func (d *downloader) Download(ctx context.Context) error {
	// Compute directory paths from the base directory. Trades go to data/ (persistent),
	// everything else goes to cache/ (blow-away safe).
	dataAccountsDir := ibctlpath.DataAccountsDirPath(d.config.DirPath)
	cacheAccountsDir := ibctlpath.CacheAccountsDirPath(d.config.DirPath)
	cacheFXDir := ibctlpath.CacheFXDirPath(d.config.DirPath)
	// Create the directory structure.
	if err := os.MkdirAll(dataAccountsDir, 0o755); err != nil {
		return fmt.Errorf("creating data accounts directory: %w", err)
	}
	if err := os.MkdirAll(cacheAccountsDir, 0o755); err != nil {
		return fmt.Errorf("creating cache accounts directory: %w", err)
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
		// Create per-account directories under both data and cache.
		dataAccountDir := filepath.Join(dataAccountsDir, alias)
		cacheAccountDir := filepath.Join(cacheAccountsDir, alias)
		if err := os.MkdirAll(dataAccountDir, 0o755); err != nil {
			return fmt.Errorf("creating data account directory for %s: %w", alias, err)
		}
		if err := os.MkdirAll(cacheAccountDir, 0o755); err != nil {
			return fmt.Errorf("creating cache account directory for %s: %w", alias, err)
		}
		// Process and write account-specific data.
		trades, err := d.processAccountData(alias, dataAccountDir, cacheAccountDir, &statement)
		if err != nil {
			return fmt.Errorf("processing account %s: %w", alias, err)
		}
		allTrades = append(allTrades, trades...)
	}
	// Eagerly download FX rates for all non-USD currencies found in trades.
	// Rates are stored per pair in fx/{BASE}.{QUOTE}/rates.json.
	if err := d.downloadFXRates(ctx, cacheFXDir, allTrades); err != nil {
		d.logger.Warn("failed to download FX rates", "error", err)
	}
	d.logger.Info("download complete")
	return nil
}

// processAccountData converts XML data to protos, merges with existing cache,
// and writes per-account data files. Trades go to dataAccountDir (persistent),
// all other snapshots go to cacheAccountDir (blow-away safe).
// Returns the merged trades for FX rate processing.
func (d *downloader) processAccountData(alias string, dataAccountDir string, cacheAccountDir string, statement *ibkrflexquery.FlexStatement) ([]*datav1.Trade, error) {
	// Convert and merge trades — written to persistent data directory.
	newTrades, err := d.convertTrades(statement.Trades, alias)
	if err != nil {
		return nil, err
	}
	trades := d.mergeTradesWithCache(newTrades, dataAccountDir)
	tradesPath := filepath.Join(dataAccountDir, "trades.json")
	if err := protoio.WriteMessagesJSON(tradesPath, trades); err != nil {
		return nil, fmt.Errorf("writing trades: %w", err)
	}
	// All remaining data is snapshot-based and goes to cache directory.
	// Positions are always overwritten with the latest snapshot.
	positions, err := d.convertPositions(statement.OpenPositions, alias)
	if err != nil {
		return nil, err
	}
	positionsPath := filepath.Join(cacheAccountDir, "positions.json")
	if err := protoio.WriteMessagesJSON(positionsPath, positions); err != nil {
		return nil, fmt.Errorf("writing positions: %w", err)
	}
	// Convert and write transfers.
	transfers, err := d.convertTransfers(statement.Transfers, alias)
	if err != nil {
		return nil, err
	}
	transfersPath := filepath.Join(cacheAccountDir, "transfers.json")
	if err := protoio.WriteMessagesJSON(transfersPath, transfers); err != nil {
		return nil, fmt.Errorf("writing transfers: %w", err)
	}
	// Convert and write trade transfers.
	tradeTransfers, err := d.convertTradeTransfers(statement.TradeTransfers, alias)
	if err != nil {
		return nil, err
	}
	tradeTransfersPath := filepath.Join(cacheAccountDir, "trade_transfers.json")
	if err := protoio.WriteMessagesJSON(tradeTransfersPath, tradeTransfers); err != nil {
		return nil, fmt.Errorf("writing trade transfers: %w", err)
	}
	// Convert and write corporate actions.
	corporateActions, err := d.convertCorporateActions(statement.CorporateActions, alias)
	if err != nil {
		return nil, err
	}
	corporateActionsPath := filepath.Join(cacheAccountDir, "corporate_actions.json")
	if err := protoio.WriteMessagesJSON(corporateActionsPath, corporateActions); err != nil {
		return nil, fmt.Errorf("writing corporate actions: %w", err)
	}
	// Convert and write cash positions from the Cash Report section.
	cashPositions := d.convertCashPositions(statement.CashReport, alias)
	cashPositionsPath := filepath.Join(cacheAccountDir, "cash_positions.json")
	if err := protoio.WriteMessagesJSON(cashPositionsPath, cashPositions); err != nil {
		return nil, fmt.Errorf("writing cash positions: %w", err)
	}
	d.logger.Info("account data written",
		"account", alias,
		"trades", len(trades),
		"positions", len(positions),
		"transfers", len(transfers),
		"trade_transfers", len(tradeTransfers),
		"corporate_actions", len(corporateActions),
		"cash_positions", len(cashPositions),
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

// downloadFXRates eagerly downloads FX rates for all non-USD currencies found
// across all data sources (Flex Query trades, seed transactions, Activity
// Statement CSVs). Rates are stored per pair in fx/{BASE}.{QUOTE}/rates.json.
// Only fetches rates for dates not already cached.
func (d *downloader) downloadFXRates(ctx context.Context, fxDirPath string, flexQueryTrades []*datav1.Trade) error {
	if err := os.MkdirAll(fxDirPath, 0o755); err != nil {
		return fmt.Errorf("creating fx directory: %w", err)
	}
	// Collect all non-USD currencies and date range across ALL data sources:
	// Flex Query trades, seed transactions, and Activity Statement CSVs.
	currencies := make(map[string]bool)
	var earliestDate, latestDate string
	// Helper to track a currency and date from any data source.
	trackCurrencyDate := func(currency string, dateStr string) {
		if currency != "" && currency != "USD" {
			currencies[currency] = true
		}
		if dateStr == "" {
			return
		}
		if earliestDate == "" || dateStr < earliestDate {
			earliestDate = dateStr
		}
		if latestDate == "" || dateStr > latestDate {
			latestDate = dateStr
		}
	}
	// Source 1: Flex Query trades (passed in from the download).
	for _, trade := range flexQueryTrades {
		trackCurrencyDate(trade.GetCurrencyCode(), tradeDateString(trade))
	}
	// Source 2: Seed transactions from previous brokers.
	seedDirPath := ibctlpath.SeedDirPath(d.config.DirPath)
	if _, err := os.Stat(seedDirPath); err == nil {
		for alias := range d.config.AccountAliases {
			seedTxnPath := filepath.Join(seedDirPath, alias, "transactions.json")
			importedTxns, err := protoio.ReadMessagesJSON(seedTxnPath, func() *datav1.ImportedTransaction { return &datav1.ImportedTransaction{} })
			if err != nil {
				continue
			}
			for _, txn := range importedTxns {
				dateStr := ""
				if d := txn.GetDate(); d != nil {
					dateStr = fmt.Sprintf("%04d-%02d-%02d", d.GetYear(), d.GetMonth(), d.GetDay())
				}
				trackCurrencyDate(txn.GetCurrencyCode(), dateStr)
			}
		}
	}
	// Source 3: Activity Statement CSV trades.
	activityStatementsDirPath := ibctlpath.ActivityStatementsDirPath(d.config.DirPath)
	if _, err := os.Stat(activityStatementsDirPath); err == nil {
		for alias := range d.config.AccountAliases {
			csvDir := filepath.Join(activityStatementsDirPath, alias)
			csvStatements, err := ibkractivitycsv.ParseDirectory(csvDir)
			if err != nil {
				continue
			}
			for _, statement := range csvStatements {
				for i := range statement.Trades {
					csvTrade := &statement.Trades[i]
					dateStr := csvTrade.DateTime.Format("2006-01-02")
					trackCurrencyDate(csvTrade.CurrencyCode, dateStr)
				}
			}
		}
	}
	if len(currencies) == 0 || earliestDate == "" {
		d.logger.Info("no non-USD currencies found in any data source, skipping FX rate download")
		return nil
	}
	d.logger.Info("FX rate date range determined",
		"earliest", earliestDate,
		"latest", latestDate,
		"currencies", fmt.Sprintf("%v", currencySet(currencies)),
	)
	// Use today's date as the end date for eager download.
	today := time.Now().Format("2006-01-02")
	if today > latestDate {
		latestDate = today
	}
	// For each non-USD currency, download X→USD rates from frankfurter.dev.
	// For each non-CAD currency (including USD), download X→CAD rates from Bank of Canada.
	// The currency set always includes CAD (from FX trades), so we always get USD.CAD.
	type pairSpec struct {
		base     string
		quote    string
		provider string // "frankfurter" or "bankofcanada"
	}
	var pairs []pairSpec
	for currency := range currencies {
		if currency != "USD" {
			// Non-USD, non-CAD currencies need X→USD from frankfurter.
			pairs = append(pairs, pairSpec{base: currency, quote: "USD", provider: "frankfurter"})
		}
		if currency != "CAD" {
			// Non-CAD currencies need X→CAD from Bank of Canada.
			pairs = append(pairs, pairSpec{base: currency, quote: "CAD", provider: "bankofcanada"})
		}
	}
	// Always download USD→CAD from Bank of Canada.
	pairs = append(pairs, pairSpec{base: "USD", quote: "CAD", provider: "bankofcanada"})
	// Fetch and write rates for each pair.
	for _, pair := range pairs {
		if err := d.downloadPairRates(ctx, fxDirPath, pair.base, pair.quote, pair.provider, earliestDate, latestDate); err != nil {
			d.logger.Warn("failed to download FX rates for pair",
				"pair", pair.base+"."+pair.quote,
				"provider", pair.provider,
				"error", err,
			)
		}
	}
	return nil
}

// downloadPairRates downloads FX rates for a single currency pair from the
// specified provider, merges with existing cached rates, and writes the result.
// Only dates not already in the cache are fetched.
func (d *downloader) downloadPairRates(ctx context.Context, fxDirPath string, base string, quote string, provider string, startDate string, endDate string) error {
	pairKey := base + "." + quote
	pairDir := filepath.Join(fxDirPath, pairKey)
	if err := os.MkdirAll(pairDir, 0o755); err != nil {
		return fmt.Errorf("creating pair directory: %w", err)
	}
	ratesPath := filepath.Join(pairDir, "rates.json")
	// Load existing cached rates for this pair.
	cachedRates, _ := protoio.ReadMessagesJSON(ratesPath, func() *datav1.ExchangeRate { return &datav1.ExchangeRate{} })
	// Determine the date range covered by cached rates.
	var cachedEarliest, cachedLatest string
	for _, rate := range cachedRates {
		dateStr := exchangeRateDateString(rate)
		if cachedEarliest == "" || dateStr < cachedEarliest {
			cachedEarliest = dateStr
		}
		if cachedLatest == "" || dateStr > cachedLatest {
			cachedLatest = dateStr
		}
	}
	// Skip the API call if cached rates already cover the requested range.
	// The latest cached rate must be within 4 days of the end date to account
	// for weekends and holidays when no rates are published.
	if cachedEarliest != "" && cachedEarliest <= startDate && cachedLatest != "" {
		latestCached, err := time.Parse("2006-01-02", cachedLatest)
		endParsed, err2 := time.Parse("2006-01-02", endDate)
		if err == nil && err2 == nil && endParsed.Sub(latestCached).Hours() <= 96 {
			d.logger.Info("FX rates already cached", "pair", pairKey, "cached_range", cachedEarliest+".."+cachedLatest)
			return nil
		}
	}
	// Fetch rates from the appropriate provider for the full date range.
	d.logger.Info("downloading FX rates", "pair", pairKey, "provider", provider, "start", startDate, "end", endDate)
	// fetchedRates holds the structured daily rates from the API.
	type dailyRate struct {
		date string
		rate *datav1.ExchangeRate
	}
	var fetchedRates []dailyRate
	switch provider {
	case "frankfurter":
		rates, err := d.fxRateClient.GetRates(ctx, base, quote, startDate, endDate)
		if err != nil {
			return fmt.Errorf("fetching rates from frankfurter: %w", err)
		}
		for _, r := range rates {
			parsedDate, err := time.Parse("2006-01-02", r.Date)
			if err != nil {
				continue
			}
			protoDate, err := timepb.NewProtoDate(parsedDate.Year(), parsedDate.Month(), parsedDate.Day())
			if err != nil {
				continue
			}
			fetchedRates = append(fetchedRates, dailyRate{
				date: r.Date,
				rate: &datav1.ExchangeRate{
					Date:              protoDate,
					BaseCurrencyCode:  base,
					QuoteCurrencyCode: quote,
					Rate:              r.Rate,
					Provider:          provider,
				},
			})
		}
	case "bankofcanada":
		rates, err := d.bocClient.GetRates(ctx, base, startDate, endDate)
		if err != nil {
			return fmt.Errorf("fetching rates from bankofcanada: %w", err)
		}
		for _, r := range rates {
			parsedDate, err := time.Parse("2006-01-02", r.Date)
			if err != nil {
				continue
			}
			protoDate, err := timepb.NewProtoDate(parsedDate.Year(), parsedDate.Month(), parsedDate.Day())
			if err != nil {
				continue
			}
			fetchedRates = append(fetchedRates, dailyRate{
				date: r.Date,
				rate: &datav1.ExchangeRate{
					Date:              protoDate,
					BaseCurrencyCode:  base,
					QuoteCurrencyCode: quote,
					Rate:              r.Rate,
					Provider:          provider,
				},
			})
		}
	default:
		return fmt.Errorf("unknown provider: %s", provider)
	}
	// Merge fetched rates with cached rates (existing dates are not overwritten).
	rateMap := make(map[string]*datav1.ExchangeRate, len(cachedRates)+len(fetchedRates))
	for _, rate := range cachedRates {
		rateMap[exchangeRateDateString(rate)] = rate
	}
	for _, fetched := range fetchedRates {
		// Skip dates already in cache — cached data takes precedence.
		if _, ok := rateMap[fetched.date]; ok {
			continue
		}
		rateMap[fetched.date] = fetched.rate
	}
	// Collect, sort, and write.
	merged := make([]*datav1.ExchangeRate, 0, len(rateMap))
	for _, rate := range rateMap {
		merged = append(merged, rate)
	}
	sort.Slice(merged, func(i, j int) bool {
		return exchangeRateDateString(merged[i]) < exchangeRateDateString(merged[j])
	})
	if err := protoio.WriteMessagesJSON(ratesPath, merged); err != nil {
		return fmt.Errorf("writing rates: %w", err)
	}
	d.logger.Info("FX rates written", "pair", pairKey, "cached", len(cachedRates), "fetched", len(fetchedRates), "total", len(merged))
	return nil
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

// convertCashPositions converts XML cash report entries to CashPosition protos.
// Filters out zero-balance currencies and the BASE_SUMMARY row.
func (d *downloader) convertCashPositions(xmlCashReport []ibkrflexquery.XMLCashReportCurrency, accountAlias string) []*datav1.CashPosition {
	var cashPositions []*datav1.CashPosition
	for _, cr := range xmlCashReport {
		// Skip the BASE_SUMMARY aggregate row.
		if cr.Currency == "BASE_SUMMARY" {
			continue
		}
		// Use EndingCash as the balance (includes unsettled trades to match IBKR portal).
		if cr.EndingCash == "" || cr.EndingCash == "0" {
			continue
		}
		balance, err := moneypb.NewProtoMoney(cr.Currency, cr.EndingCash)
		if err != nil {
			d.logger.Warn("skipping unparseable cash position", "currency", cr.Currency, "error", err)
			continue
		}
		// Skip zero balances after parsing.
		if moneypb.MoneyToMicros(balance) == 0 {
			continue
		}
		cashPositions = append(cashPositions, &datav1.CashPosition{
			AccountId: accountAlias,
			Balance:   balance,
		})
	}
	return cashPositions
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
	// Parse the position quantity as a decimal (IBKR uses "position" attribute, not "quantity").
	quantity, err := mathpb.NewDecimal(xmlPosition.Position)
	if err != nil {
		return nil, fmt.Errorf("parsing position quantity %q: %w", xmlPosition.Position, err)
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

// currencySet converts a map[string]bool to a sorted slice for logging.
func currencySet(m map[string]bool) []string {
	var result []string
	for k := range m {
		result = append(result, k)
	}
	sort.Strings(result)
	return result
}
