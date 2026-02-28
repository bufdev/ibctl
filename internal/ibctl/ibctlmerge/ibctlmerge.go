// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctlmerge merges trade data from Activity Statement CSVs (seed data)
// and the Flex Query cache (supplement) into a unified view for commands to use.
package ibctlmerge

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	datav1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/data/v1"
	"github.com/bufdev/ibctl/internal/pkg/ibkractivitycsv"
	"github.com/bufdev/ibctl/internal/pkg/moneypb"
	"github.com/bufdev/ibctl/internal/pkg/protoio"
	"github.com/bufdev/ibctl/internal/pkg/timepb"
)

// MergedData contains all trade data merged from Activity Statement CSVs and Flex Query cache.
type MergedData struct {
	// Trades is the deduplicated, sorted list of all trades.
	Trades []*datav1.Trade
	// Positions is the most recent set of open positions (from Flex Query cache if available,
	// otherwise from the most recent Activity Statement CSV).
	Positions []*datav1.Position
}

// Merge reads Activity Statement CSVs and Flex Query cached data, deduplicates trades
// by a composite key (Symbol + DateTime + Quantity + Price), and returns the merged result.
// The flexCacheDirPath may be empty if no Flex Query data has been downloaded yet.
func Merge(csvStatements []*ibkractivitycsv.ActivityStatement, flexCacheDirPath string) (*MergedData, error) {
	// Load Flex Query cached trades first (these are the baseline).
	tradeMap := make(map[string]*datav1.Trade)
	var flexPositions []*datav1.Position
	if flexCacheDirPath != "" {
		tradesPath := filepath.Join(flexCacheDirPath, "trades.json")
		cachedTrades, err := protoio.ReadMessagesJSON(tradesPath, func() *datav1.Trade { return &datav1.Trade{} })
		if err == nil {
			for _, trade := range cachedTrades {
				key := tradeKey(trade)
				tradeMap[key] = trade
			}
		}
		// Read Flex Query positions as baseline.
		positionsPath := filepath.Join(flexCacheDirPath, "positions.json")
		flexPos, err := protoio.ReadMessagesJSON(positionsPath, func() *datav1.Position { return &datav1.Position{} })
		if err == nil {
			flexPositions = flexPos
		}
	}
	// Merge in Activity Statement CSV trades on top — CSV data takes precedence
	// over Flex Query data since the user manages the CSVs directly and may
	// download updated statements that supersede cached API data.
	var csvPositions []*datav1.Position
	for _, statement := range csvStatements {
		for i := range statement.Trades {
			trade, err := csvTradeToProto(&statement.Trades[i])
			if err != nil {
				continue // Skip unparseable trades.
			}
			// CSV trades overwrite Flex Query trades with the same key.
			key := tradeKey(trade)
			tradeMap[key] = trade
		}
		// Accumulate positions from CSVs (latest wins by overwrite).
		for i := range statement.Positions {
			pos, err := csvPositionToProto(&statement.Positions[i])
			if err != nil {
				continue
			}
			csvPositions = append(csvPositions, pos)
		}
	}
	// Collect and sort trades for deterministic output.
	trades := make([]*datav1.Trade, 0, len(tradeMap))
	for _, trade := range tradeMap {
		trades = append(trades, trade)
	}
	sort.Slice(trades, func(i, j int) bool {
		dateI := protoDateString(trades[i].GetTradeDate())
		dateJ := protoDateString(trades[j].GetTradeDate())
		if dateI != dateJ {
			return dateI < dateJ
		}
		return trades[i].GetTradeId() < trades[j].GetTradeId()
	})
	// Prefer CSV positions over Flex Query positions (user-managed data takes precedence).
	// Fall back to Flex Query positions if no CSVs are configured.
	positions := csvPositions
	if len(positions) == 0 {
		positions = flexPositions
	}
	return &MergedData{
		Trades:    trades,
		Positions: positions,
	}, nil
}

// csvTradeToProto converts an Activity Statement CSV trade to a proto Trade.
func csvTradeToProto(csvTrade *ibkractivitycsv.Trade) (*datav1.Trade, error) {
	// Parse quantity as decimal (supports fractional shares).
	quantityUnits, quantityMicros, err := moneypb.ParseDecimalToUnitsMicros(csvTrade.Quantity)
	if err != nil {
		return nil, fmt.Errorf("parsing quantity %q: %w", csvTrade.Quantity, err)
	}
	// Determine buy/sell from quantity sign.
	side := datav1.TradeSide_TRADE_SIDE_BUY
	if quantityUnits < 0 || (quantityUnits == 0 && quantityMicros < 0) {
		side = datav1.TradeSide_TRADE_SIDE_SELL
	}
	// Parse the trade date into a proto Date.
	protoDate, err := timepb.NewProtoDate(csvTrade.DateTime.Year(), csvTrade.DateTime.Month(), csvTrade.DateTime.Day())
	if err != nil {
		return nil, err
	}
	// Parse monetary values.
	currencyCode := csvTrade.CurrencyCode
	tradePrice, err := moneypb.NewProtoMoney(currencyCode, csvTrade.TradePrice)
	if err != nil {
		return nil, fmt.Errorf("parsing trade price: %w", err)
	}
	proceeds, err := moneypb.NewProtoMoney(currencyCode, csvTrade.Proceeds)
	if err != nil {
		return nil, fmt.Errorf("parsing proceeds: %w", err)
	}
	commission, err := moneypb.NewProtoMoney(currencyCode, csvTrade.Commission)
	if err != nil {
		return nil, fmt.Errorf("parsing commission: %w", err)
	}
	// Generate a deterministic trade ID from the composite key since CSVs don't have one.
	tradeID := generateTradeID(csvTrade.Symbol, csvTrade.DateTime, csvTrade.Quantity, csvTrade.TradePrice)
	return &datav1.Trade{
		TradeId:        tradeID,
		TradeDate:      protoDate,
		SettleDate:     protoDate, // CSV doesn't have settle date, use trade date.
		Symbol:         csvTrade.Symbol,
		Side:           side,
		QuantityUnits:  quantityUnits,
		QuantityMicros: quantityMicros,
		TradePrice:     tradePrice,
		Proceeds:       proceeds,
		Commission:     commission,
		CurrencyCode:   currencyCode,
	}, nil
}

// csvPositionToProto converts an Activity Statement CSV position to a proto Position.
func csvPositionToProto(csvPosition *ibkractivitycsv.Position) (*datav1.Position, error) {
	currencyCode := csvPosition.CurrencyCode
	quantityUnits, quantityMicros, err := moneypb.ParseDecimalToUnitsMicros(csvPosition.Quantity)
	if err != nil {
		return nil, fmt.Errorf("parsing quantity %q: %w", csvPosition.Quantity, err)
	}
	costBasisPrice, err := moneypb.NewProtoMoney(currencyCode, csvPosition.CostPrice)
	if err != nil {
		return nil, fmt.Errorf("parsing cost price: %w", err)
	}
	marketPrice, err := moneypb.NewProtoMoney(currencyCode, csvPosition.ClosePrice)
	if err != nil {
		return nil, fmt.Errorf("parsing close price: %w", err)
	}
	marketValue, err := moneypb.NewProtoMoney(currencyCode, csvPosition.Value)
	if err != nil {
		return nil, fmt.Errorf("parsing value: %w", err)
	}
	return &datav1.Position{
		Symbol:         csvPosition.Symbol,
		AssetCategory:  csvPosition.AssetCategory,
		QuantityUnits:  quantityUnits,
		QuantityMicros: quantityMicros,
		CostBasisPrice: costBasisPrice,
		MarketPrice:    marketPrice,
		MarketValue:    marketValue,
		CurrencyCode:   currencyCode,
	}, nil
}

// tradeKey generates a deterministic dedup key for a trade proto.
// Uses TradeId if available (Flex Query trades), otherwise Symbol+Date+Quantity+Price.
func tradeKey(trade *datav1.Trade) string {
	if trade.GetTradeId() != "" && !strings.HasPrefix(trade.GetTradeId(), "csv-") {
		// Flex Query trades have real IBKR trade IDs.
		return "ibkr-" + trade.GetTradeId()
	}
	// CSV-derived trades use composite key.
	return fmt.Sprintf("csv-%s-%s-%d.%d-%s",
		trade.GetSymbol(),
		protoDateString(trade.GetTradeDate()),
		trade.GetQuantityUnits(),
		trade.GetQuantityMicros(),
		moneypb.MoneyValueToString(trade.GetTradePrice()),
	)
}

// generateTradeID creates a deterministic trade ID from trade fields.
// Uses a hash prefix to keep it short while avoiding collisions.
func generateTradeID(symbol string, dateTime time.Time, quantity string, price string) string {
	raw := fmt.Sprintf("%s|%s|%s|%s", symbol, dateTime.Format(time.RFC3339), quantity, price)
	hash := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("csv-%x", hash[:8])
}

// protoDateString returns a sortable date string from a proto Date.
func protoDateString(d interface {
	GetYear() uint32
	GetMonth() uint32
	GetDay() uint32
}) string {
	if d == nil {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.GetYear(), d.GetMonth(), d.GetDay())
}
