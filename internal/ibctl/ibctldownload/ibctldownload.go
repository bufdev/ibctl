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
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Downloader is the interface for downloading and caching IBKR data.
type Downloader interface {
	// Download fetches IBKR data via the Flex Query API and caches it as JSON files.
	Download(ctx context.Context) error
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

func (d *downloader) Download(ctx context.Context) error {
	// Step 1: Create the data directory if needed.
	dataDirV1 := d.dataDirV1Path
	if err := os.MkdirAll(dataDirV1, 0o755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}
	d.logger.Info("data directory ready", "path", dataDirV1)

	// Step 2: Fetch and parse the Flex Query statement.
	d.logger.Info("downloading flex query data")
	statement, err := d.flexQueryClient.Download(ctx, d.ibkrToken, d.config.IBKRQueryID)
	if err != nil {
		return fmt.Errorf("downloading flex query: %w", err)
	}
	d.logger.Info("flex query data downloaded")

	// Step 3: Convert XML trades to proto and write trades.json (newline-separated).
	trades, err := d.convertTrades(statement.Trades)
	if err != nil {
		return err
	}
	tradesPath := filepath.Join(dataDirV1, "trades.json")
	if err := protoio.WriteMessagesJSON(tradesPath, trades); err != nil {
		return fmt.Errorf("writing trades: %w", err)
	}
	d.logger.Info("trades written", "count", len(trades), "path", tradesPath)

	// Step 4: Convert XML positions to proto and write positions.json (newline-separated).
	positions, err := d.convertPositions(statement.OpenPositions)
	if err != nil {
		return err
	}
	positionsPath := filepath.Join(dataDirV1, "positions.json")
	if err := protoio.WriteMessagesJSON(positionsPath, positions); err != nil {
		return fmt.Errorf("writing positions: %w", err)
	}
	d.logger.Info("positions written", "count", len(positions), "path", positionsPath)

	// Step 5: Extract FX rates from cash transactions and write exchange_rates.json (newline-separated).
	exchangeRates := d.extractExchangeRates(statement.CashTransactions)
	if err := d.fetchMissingExchangeRates(ctx, exchangeRates, trades); err != nil {
		d.logger.Warn("failed to fetch missing exchange rates", "error", err)
	}
	exchangeRatesPath := filepath.Join(dataDirV1, "exchange_rates.json")
	if err := protoio.WriteMessagesJSON(exchangeRatesPath, exchangeRates); err != nil {
		return fmt.Errorf("writing exchange rates: %w", err)
	}
	d.logger.Info("exchange rates written", "count", len(exchangeRates), "path", exchangeRatesPath)

	// Step 6: Compute tax lots and write tax_lots.json (newline-separated).
	taxLots, err := ibctltaxlot.ComputeTaxLots(trades)
	if err != nil {
		return fmt.Errorf("computing tax lots: %w", err)
	}
	taxLotsPath := filepath.Join(dataDirV1, "tax_lots.json")
	if err := protoio.WriteMessagesJSON(taxLotsPath, taxLots); err != nil {
		return fmt.Errorf("writing tax lots: %w", err)
	}
	d.logger.Info("tax lots written", "count", len(taxLots), "path", taxLotsPath)

	// Step 7: Compute positions from tax lots.
	computedPositions := ibctltaxlot.ComputePositions(taxLots)

	// Step 8: Verify computed vs IBKR-reported positions.
	verificationNotes := ibctltaxlot.VerifyPositions(computedPositions, positions)
	positionsVerified := len(verificationNotes) == 0
	if !positionsVerified {
		for _, note := range verificationNotes {
			d.logger.Warn("position verification", "note", note)
		}
	} else {
		d.logger.Info("all positions verified successfully")
	}

	// Step 9: Write metadata.json.
	metadata := &datav1.Metadata{
		DownloadTime:      timestamppb.Now(),
		PositionsVerified: positionsVerified,
		VerificationNotes: verificationNotes,
	}
	metadataPath := filepath.Join(dataDirV1, "metadata.json")
	if err := protoio.WriteMessageJSON(metadataPath, metadata); err != nil {
		return fmt.Errorf("writing metadata: %w", err)
	}
	d.logger.Info("download complete", "positions_verified", positionsVerified)
	return nil
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
