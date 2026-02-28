// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctldownload provides the download orchestrator for IBKR data.
package ibctldownload

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	exchangeratev1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/exchangerate/v1"
	metadatav1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/metadata/v1"
	positionv1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/position/v1"
	taxlotv1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/taxlot/v1"
	tradev1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/trade/v1"
	"github.com/bufdev/ibctl/internal/ibctl/ibctlconfig"
	"github.com/bufdev/ibctl/internal/ibctl/ibctltaxlot"
	"github.com/bufdev/ibctl/internal/pkg/cli"
	"github.com/bufdev/ibctl/internal/pkg/flexquery"
	"github.com/bufdev/ibctl/internal/pkg/fxrate"
	"github.com/bufdev/ibctl/internal/pkg/timepb"
)

// Downloader is the interface for downloading and caching IBKR data.
type Downloader interface {
	// Download fetches IBKR data via the Flex Query API and caches it as JSON files.
	Download(ctx context.Context) error
}

// DownloaderOption is a functional option for configuring the Downloader.
type DownloaderOption func(*downloader)

// WithLogger sets the logger for the downloader.
func WithLogger(logger *slog.Logger) DownloaderOption {
	return func(d *downloader) {
		d.logger = logger
	}
}

// WithFlexQueryClient sets a custom Flex Query API client.
func WithFlexQueryClient(client flexquery.Client) DownloaderOption {
	return func(d *downloader) {
		d.flexQueryClient = client
	}
}

// WithFXRateClient sets a custom exchange rate client.
func WithFXRateClient(client fxrate.Client) DownloaderOption {
	return func(d *downloader) {
		d.fxRateClient = client
	}
}

// NewDownloader creates a new Downloader with the given configuration and options.
func NewDownloader(config *ibctlconfig.Config, options ...DownloaderOption) Downloader {
	d := &downloader{
		config: config,
		logger: slog.Default(),
	}
	for _, option := range options {
		option(d)
	}
	// Set default clients if not provided.
	if d.flexQueryClient == nil {
		d.flexQueryClient = flexquery.NewClient(
			flexquery.ClientWithLogger(d.logger),
		)
	}
	if d.fxRateClient == nil {
		d.fxRateClient = fxrate.NewClient()
	}
	return d
}

type downloader struct {
	config          *ibctlconfig.Config
	logger          *slog.Logger
	flexQueryClient flexquery.Client
	fxRateClient    fxrate.Client
}

func (d *downloader) Download(ctx context.Context) error {
	// Step 1: Create the data directory if needed.
	dataDirV1 := d.config.DataDirV1Path()
	if err := os.MkdirAll(dataDirV1, 0o755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}
	d.logger.Info("data directory ready", "path", dataDirV1)

	// Step 2: Fetch XML via the Flex Query API.
	d.logger.Info("downloading flex query data")
	xmlData, err := d.flexQueryClient.Download(ctx, d.config.IBKRToken, d.config.IBKRQueryID)
	if err != nil {
		return fmt.Errorf("downloading flex query: %w", err)
	}
	d.logger.Info("flex query data downloaded", "bytes", len(xmlData))

	// Step 3: Parse the XML response.
	response, err := flexquery.ParseFlexQueryResponse(xmlData)
	if err != nil {
		return fmt.Errorf("parsing flex query response: %w", err)
	}
	statement := response.FlexStatements.FlexStatement

	// Step 4: Convert XML trades to proto and write trades.json.
	trades, err := d.convertTrades(statement.Trades)
	if err != nil {
		return err
	}
	tradesProto := &tradev1.Trades{Trades: trades}
	tradesPath := filepath.Join(dataDirV1, "trades.json")
	if err := cli.WriteProtoMessageJSON(tradesPath, tradesProto); err != nil {
		return fmt.Errorf("writing trades: %w", err)
	}
	d.logger.Info("trades written", "count", len(trades), "path", tradesPath)

	// Step 5: Convert XML positions to proto and write positions.json.
	positions, err := d.convertPositions(statement.OpenPositions)
	if err != nil {
		return err
	}
	positionsProto := &positionv1.Positions{Positions: positions}
	positionsPath := filepath.Join(dataDirV1, "positions.json")
	if err := cli.WriteProtoMessageJSON(positionsPath, positionsProto); err != nil {
		return fmt.Errorf("writing positions: %w", err)
	}
	d.logger.Info("positions written", "count", len(positions), "path", positionsPath)

	// Step 6: Extract FX rates from cash transactions and write exchange_rates.json.
	exchangeRates := d.extractExchangeRates(statement.CashTransactions)
	if err := d.fetchMissingExchangeRates(ctx, exchangeRates, trades); err != nil {
		d.logger.Warn("failed to fetch missing exchange rates", "error", err)
	}
	exchangeRatesProto := &exchangeratev1.ExchangeRates{ExchangeRates: exchangeRates}
	exchangeRatesPath := filepath.Join(dataDirV1, "exchange_rates.json")
	if err := cli.WriteProtoMessageJSON(exchangeRatesPath, exchangeRatesProto); err != nil {
		return fmt.Errorf("writing exchange rates: %w", err)
	}
	d.logger.Info("exchange rates written", "count", len(exchangeRates), "path", exchangeRatesPath)

	// Step 7: Compute tax lots and write tax_lots.json.
	taxLots, err := ibctltaxlot.ComputeTaxLots(trades)
	if err != nil {
		return fmt.Errorf("computing tax lots: %w", err)
	}
	taxLotsProto := &taxlotv1.TaxLots{TaxLots: taxLots}
	taxLotsPath := filepath.Join(dataDirV1, "tax_lots.json")
	if err := cli.WriteProtoMessageJSON(taxLotsPath, taxLotsProto); err != nil {
		return fmt.Errorf("writing tax lots: %w", err)
	}
	d.logger.Info("tax lots written", "count", len(taxLots), "path", taxLotsPath)

	// Step 8: Compute positions from tax lots.
	computedPositions := ibctltaxlot.ComputePositions(taxLots)

	// Step 9: Verify computed vs IBKR-reported positions.
	verificationNotes := ibctltaxlot.VerifyPositions(computedPositions, positions)
	positionsVerified := len(verificationNotes) == 0
	if !positionsVerified {
		for _, note := range verificationNotes {
			d.logger.Warn("position verification", "note", note)
		}
	} else {
		d.logger.Info("all positions verified successfully")
	}

	// Step 10: Write metadata.json.
	metadata := &metadatav1.Metadata{
		DownloadTime:      time.Now().Format(time.RFC3339),
		PositionsVerified: positionsVerified,
		VerificationNotes: verificationNotes,
	}
	metadataPath := filepath.Join(dataDirV1, "metadata.json")
	if err := cli.WriteProtoMessageJSON(metadataPath, metadata); err != nil {
		return fmt.Errorf("writing metadata: %w", err)
	}
	d.logger.Info("download complete", "positions_verified", positionsVerified)
	return nil
}

// convertTrades converts XML trades to proto trades.
func (d *downloader) convertTrades(xmlTrades []flexquery.XMLTrade) ([]*tradev1.Trade, error) {
	trades := make([]*tradev1.Trade, 0, len(xmlTrades))
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
func (d *downloader) convertPositions(xmlPositions []flexquery.XMLPosition) ([]*positionv1.Position, error) {
	positions := make([]*positionv1.Position, 0, len(xmlPositions))
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
func (d *downloader) extractExchangeRates(cashTransactions []flexquery.XMLCashTransaction) []*exchangeratev1.ExchangeRate {
	// Use a set to avoid duplicate rates for the same date/currency pair.
	type rateKey struct {
		date          string
		baseCurrency  string
		quoteCurrency string
	}
	seen := make(map[rateKey]bool)
	var rates []*exchangeratev1.ExchangeRate
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
		rates = append(rates, &exchangeratev1.ExchangeRate{
			Date:          protoDate,
			BaseCurrency:  ct.Currency,
			QuoteCurrency: "USD",
			Rate:          ct.FxRateToBase,
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
func (d *downloader) fetchMissingExchangeRates(ctx context.Context, existingRates []*exchangeratev1.ExchangeRate, trades []*tradev1.Trade) error {
	// Build set of existing rate dates.
	type rateKey struct {
		date     string
		currency string
	}
	existingSet := make(map[rateKey]bool)
	for _, rate := range existingRates {
		dateStr := fmt.Sprintf("%04d-%02d-%02d", rate.GetDate().GetYear(), rate.GetDate().GetMonth(), rate.GetDate().GetDay())
		existingSet[rateKey{date: dateStr, currency: rate.GetBaseCurrency()}] = true
	}
	// Find trade dates with non-USD currencies that don't have FX rates.
	missingDates := make(map[string]map[string]bool) // currency -> set of dates
	for _, trade := range trades {
		if trade.GetCurrency() == "USD" {
			continue
		}
		dateStr := fmt.Sprintf("%04d-%02d-%02d", trade.GetTradeDate().GetYear(), trade.GetTradeDate().GetMonth(), trade.GetTradeDate().GetDay())
		key := rateKey{date: dateStr, currency: trade.GetCurrency()}
		if existingSet[key] {
			continue
		}
		if missingDates[trade.GetCurrency()] == nil {
			missingDates[trade.GetCurrency()] = make(map[string]bool)
		}
		missingDates[trade.GetCurrency()][dateStr] = true
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
			existingRates = append(existingRates, &exchangeratev1.ExchangeRate{
				Date:          protoDate,
				BaseCurrency:  currency,
				QuoteCurrency: "USD",
				Rate:          rate,
			})
		}
	}
	return nil
}
