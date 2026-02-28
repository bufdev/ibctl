// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctlmerge merges trade data from Activity Statement CSVs (seed data)
// and the Flex Query cache (supplement) into a unified view for commands to use.
// Data is organized per account using account aliases.
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
	"github.com/bufdev/ibctl/internal/pkg/mathpb"
	"github.com/bufdev/ibctl/internal/pkg/moneypb"
	"github.com/bufdev/ibctl/internal/pkg/protoio"
	"github.com/bufdev/ibctl/internal/pkg/timepb"
)

// MergedData contains all data merged from Activity Statement CSVs and Flex Query cache
// across all accounts.
type MergedData struct {
	// Trades is the deduplicated, sorted list of all trades across all accounts.
	Trades []*datav1.Trade
	// Positions is the most recent set of open positions across all accounts.
	Positions []*datav1.Position
	// Transfers is the list of position transfers across all accounts.
	Transfers []*datav1.Transfer
	// TradeTransfers is the list of transferred trade cost basis records across all accounts.
	TradeTransfers []*datav1.TradeTransfer
	// CorporateActions is the list of corporate action events across all accounts.
	CorporateActions []*datav1.CorporateAction
}

// Merge reads Activity Statement CSVs and Flex Query cached data for all accounts,
// deduplicates trades, and returns the merged result.
// The dataDirV1Path is the versioned data directory containing per-account subdirectories.
// The activityStatementsDirPath is the directory containing per-account CSV subdirectories.
// The accountAliases map contains alias → account ID mappings from config.
func Merge(
	dataDirV1Path string,
	activityStatementsDirPath string,
	accountAliases map[string]string,
) (*MergedData, error) {
	tradeMap := make(map[string]*datav1.Trade)
	var allPositions []*datav1.Position
	var allTransfers []*datav1.Transfer
	var allTradeTransfers []*datav1.TradeTransfer
	var allCorporateActions []*datav1.CorporateAction
	// Process each account: load Flex Query cache + Activity Statement CSVs.
	for alias := range accountAliases {
		// Load Flex Query cached data for this account.
		accountDir := filepath.Join(dataDirV1Path, alias)
		tradesPath := filepath.Join(accountDir, "trades.json")
		cachedTrades, err := protoio.ReadMessagesJSON(tradesPath, func() *datav1.Trade { return &datav1.Trade{} })
		if err == nil {
			for _, trade := range cachedTrades {
				tradeMap[tradeKey(trade)] = trade
			}
		}
		positionsPath := filepath.Join(accountDir, "positions.json")
		positions, err := protoio.ReadMessagesJSON(positionsPath, func() *datav1.Position { return &datav1.Position{} })
		if err == nil {
			allPositions = append(allPositions, positions...)
		}
		transfersPath := filepath.Join(accountDir, "transfers.json")
		transfers, err := protoio.ReadMessagesJSON(transfersPath, func() *datav1.Transfer { return &datav1.Transfer{} })
		if err == nil {
			allTransfers = append(allTransfers, transfers...)
		}
		tradeTransfersPath := filepath.Join(accountDir, "trade_transfers.json")
		tradeTransfers, err := protoio.ReadMessagesJSON(tradeTransfersPath, func() *datav1.TradeTransfer { return &datav1.TradeTransfer{} })
		if err == nil {
			allTradeTransfers = append(allTradeTransfers, tradeTransfers...)
		}
		corporateActionsPath := filepath.Join(accountDir, "corporate_actions.json")
		corporateActions, err := protoio.ReadMessagesJSON(corporateActionsPath, func() *datav1.CorporateAction { return &datav1.CorporateAction{} })
		if err == nil {
			allCorporateActions = append(allCorporateActions, corporateActions...)
		}
		// Load Activity Statement CSVs for this account from the matching subdirectory.
		// CSV data takes precedence over Flex Query data.
		csvDir := filepath.Join(activityStatementsDirPath, alias)
		csvStatements, err := ibkractivitycsv.ParseDirectory(csvDir)
		if err != nil {
			// No CSV directory for this account — skip (not an error).
			continue
		}
		for _, statement := range csvStatements {
			for i := range statement.Trades {
				trade, err := csvTradeToProto(&statement.Trades[i], alias)
				if err != nil {
					continue
				}
				// CSV trades overwrite Flex Query trades with the same key.
				tradeMap[tradeKey(trade)] = trade
			}
			// CSV positions overwrite Flex Query positions for this account.
			for i := range statement.Positions {
				pos, err := csvPositionToProto(&statement.Positions[i], alias)
				if err != nil {
					continue
				}
				allPositions = append(allPositions, pos)
			}
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
	return &MergedData{
		Trades:           trades,
		Positions:        allPositions,
		Transfers:        allTransfers,
		TradeTransfers:   allTradeTransfers,
		CorporateActions: allCorporateActions,
	}, nil
}

// csvTradeToProto converts an Activity Statement CSV trade to a proto Trade.
// The accountAlias is derived from the CSV subdirectory name.
func csvTradeToProto(csvTrade *ibkractivitycsv.Trade, accountAlias string) (*datav1.Trade, error) {
	// Parse quantity as decimal (supports fractional shares).
	quantity, err := mathpb.NewDecimal(csvTrade.Quantity)
	if err != nil {
		return nil, fmt.Errorf("parsing quantity %q: %w", csvTrade.Quantity, err)
	}
	// Determine buy/sell from quantity sign.
	side := datav1.TradeSide_TRADE_SIDE_BUY
	if mathpb.ToMicros(quantity) < 0 {
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
		TradeId:      tradeID,
		AccountId:    accountAlias,
		TradeDate:    protoDate,
		SettleDate:   protoDate, // CSV doesn't have settle date, use trade date.
		Symbol:       csvTrade.Symbol,
		Side:         side,
		Quantity:     quantity,
		TradePrice:   tradePrice,
		Proceeds:     proceeds,
		Commission:   commission,
		CurrencyCode: currencyCode,
	}, nil
}

// csvPositionToProto converts an Activity Statement CSV position to a proto Position.
// The accountAlias is derived from the CSV subdirectory name.
func csvPositionToProto(csvPosition *ibkractivitycsv.Position, accountAlias string) (*datav1.Position, error) {
	currencyCode := csvPosition.CurrencyCode
	quantity, err := mathpb.NewDecimal(csvPosition.Quantity)
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
		AccountId:      accountAlias,
		AssetCategory:  csvPosition.AssetCategory,
		Quantity:       quantity,
		CostBasisPrice: costBasisPrice,
		MarketPrice:    marketPrice,
		MarketValue:    marketValue,
		CurrencyCode:   currencyCode,
	}, nil
}

// tradeKey generates a deterministic dedup key for a trade proto.
// Uses TradeId if available (Flex Query trades), otherwise Symbol+Date+Quantity+Price.
// Includes account_id in the key so trades from different accounts don't collide.
func tradeKey(trade *datav1.Trade) string {
	accountPrefix := trade.GetAccountId()
	if accountPrefix == "" {
		accountPrefix = "unknown"
	}
	if trade.GetTradeId() != "" && !strings.HasPrefix(trade.GetTradeId(), "csv-") {
		// Flex Query trades have real IBKR trade IDs, scoped per account.
		return fmt.Sprintf("%s-ibkr-%s", accountPrefix, trade.GetTradeId())
	}
	// CSV-derived trades use composite key.
	return fmt.Sprintf("%s-csv-%s-%s-%s-%s",
		accountPrefix,
		trade.GetSymbol(),
		protoDateString(trade.GetTradeDate()),
		mathpb.ToString(trade.GetQuantity()),
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
