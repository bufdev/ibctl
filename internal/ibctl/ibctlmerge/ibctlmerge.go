// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctlmerge merges trade data from Activity Statement CSVs (seed data)
// and the Flex Query cache (supplement) into a unified view for commands to use.
// Data is organized per account using account aliases.
//
// For overlapping date ranges, CSV data takes precedence because the Flex Query
// API consolidates order executions differently than Activity Statements. Flex
// Query trades are only used for dates not covered by CSVs.
package ibctlmerge

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"sort"
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
// merges them, and returns the result.
//
// For each account, CSV trades are loaded first to determine which dates they cover.
// Flex Query trades are then loaded only for dates NOT covered by the CSV data, since
// the two sources represent the same trades at different granularities (the Flex Query
// splits order executions while Activity Statements consolidate them).
func Merge(
	dataDirV1Path string,
	activityStatementsDirPath string,
	seedDirPath string,
	accountAliases map[string]string,
) (*MergedData, error) {
	var allTrades []*datav1.Trade
	var allPositions []*datav1.Position
	var allTransfers []*datav1.Transfer
	var allTradeTransfers []*datav1.TradeTransfer
	var allCorporateActions []*datav1.CorporateAction
	// Process each account: load CSVs first, then supplement with Flex Query data.
	for alias := range accountAliases {
		// Step 1: Load Activity Statement CSV trades for this account.
		// These are the primary source of truth.
		var csvTrades []*datav1.Trade
		csvDir := filepath.Join(activityStatementsDirPath, alias)
		csvStatements, err := ibkractivitycsv.ParseDirectory(csvDir)
		if err == nil {
			for _, statement := range csvStatements {
				for i := range statement.Trades {
					trade, err := csvTradeToProto(&statement.Trades[i], alias)
					if err != nil {
						continue
					}
					csvTrades = append(csvTrades, trade)
				}
				// CSV positions are historical snapshots — we use Flex Query positions
				// instead since they have current market prices. CSV positions are not
				// loaded here.
			}
		}
		// Find the date range covered by CSV trades for this account.
		// Flex Query trades within this range will be excluded.
		csvMinDate, csvMaxDate := tradeDateRange(csvTrades)
		allTrades = append(allTrades, csvTrades...)
		// Step 2: Load imported transactions from previous broker (seed data).
		// These are the complete normalized transaction history (buys, sells,
		// splits, dividends, expiries) from UBS/RBC, converted to Trade protos.
		if seedDirPath != "" {
			seedTxnPath := filepath.Join(seedDirPath, alias, "transactions.json")
			importedTxns, err := protoio.ReadMessagesJSON(seedTxnPath, func() *datav1.ImportedTransaction { return &datav1.ImportedTransaction{} })
			if err == nil {
				for _, txn := range importedTxns {
					// Only security transactions (buys, sells, splits, etc.) become trades.
					// Non-security transactions (dividends, interest, fees) return nil.
					trade := importedTransactionToTrade(txn)
					if trade != nil {
						allTrades = append(allTrades, trade)
					}
				}
			}
		}
		// Step 3: Load Flex Query cached trades, excluding dates covered by CSVs.
		accountDir := filepath.Join(dataDirV1Path, alias)
		tradesPath := filepath.Join(accountDir, "trades.json")
		cachedTrades, err := protoio.ReadMessagesJSON(tradesPath, func() *datav1.Trade { return &datav1.Trade{} })
		if err == nil {
			for _, trade := range cachedTrades {
				// Skip Flex Query trades that fall within the CSV date range,
				// since the CSV data covers those dates at the correct granularity.
				if csvMinDate != "" && csvMaxDate != "" {
					tradeDate := protoDateString(trade.GetTradeDate())
					if tradeDate >= csvMinDate && tradeDate <= csvMaxDate {
						continue
					}
				}
				allTrades = append(allTrades, trade)
			}
		}
		// Load Flex Query positions (used as fallback if no CSV positions exist).
		positionsPath := filepath.Join(accountDir, "positions.json")
		positions, err := protoio.ReadMessagesJSON(positionsPath, func() *datav1.Position { return &datav1.Position{} })
		if err == nil {
			allPositions = append(allPositions, positions...)
		}
		// Load transfers for this account.
		transfersPath := filepath.Join(accountDir, "transfers.json")
		transfers, err := protoio.ReadMessagesJSON(transfersPath, func() *datav1.Transfer { return &datav1.Transfer{} })
		if err == nil {
			allTransfers = append(allTransfers, transfers...)
		}
		// Load trade transfers for this account.
		tradeTransfersPath := filepath.Join(accountDir, "trade_transfers.json")
		tradeTransfers, err := protoio.ReadMessagesJSON(tradeTransfersPath, func() *datav1.TradeTransfer { return &datav1.TradeTransfer{} })
		if err == nil {
			allTradeTransfers = append(allTradeTransfers, tradeTransfers...)
		}
		// Load corporate actions for this account.
		corporateActionsPath := filepath.Join(accountDir, "corporate_actions.json")
		corporateActions, err := protoio.ReadMessagesJSON(corporateActionsPath, func() *datav1.CorporateAction { return &datav1.CorporateAction{} })
		if err == nil {
			allCorporateActions = append(allCorporateActions, corporateActions...)
		}
	}
	// Sort all trades by date for deterministic output.
	sort.Slice(allTrades, func(i, j int) bool {
		dateI := protoDateString(allTrades[i].GetTradeDate())
		dateJ := protoDateString(allTrades[j].GetTradeDate())
		if dateI != dateJ {
			return dateI < dateJ
		}
		// Within the same date, sort by account then symbol for stability.
		if allTrades[i].GetAccountId() != allTrades[j].GetAccountId() {
			return allTrades[i].GetAccountId() < allTrades[j].GetAccountId()
		}
		return allTrades[i].GetSymbol() < allTrades[j].GetSymbol()
	})
	return &MergedData{
		Trades:           allTrades,
		Positions:        allPositions,
		Transfers:        allTransfers,
		TradeTransfers:   allTradeTransfers,
		CorporateActions: allCorporateActions,
	}, nil
}

// tradeDateRange returns the min and max trade dates as sortable strings.
// Returns empty strings if there are no trades.
func tradeDateRange(trades []*datav1.Trade) (string, string) {
	var minDate, maxDate string
	for _, trade := range trades {
		dateStr := protoDateString(trade.GetTradeDate())
		if dateStr == "" {
			continue
		}
		if minDate == "" || dateStr < minDate {
			minDate = dateStr
		}
		if maxDate == "" || dateStr > maxDate {
			maxDate = dateStr
		}
	}
	return minDate, maxDate
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

// generateTradeID creates a deterministic trade ID from trade fields.
// Uses a hash prefix to keep it short while avoiding collisions.
func generateTradeID(symbol string, dateTime time.Time, quantity string, price string) string {
	raw := fmt.Sprintf("%s|%s|%s|%s", symbol, dateTime.Format(time.RFC3339), quantity, price)
	hash := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("csv-%x", hash[:8])
}

// importedTransactionToTrade converts an ImportedTransaction (from the previous broker)
// to a Trade proto for FIFO processing. Returns nil for non-security transactions
// (dividends, interest, fees, transfers, deposits, withdrawals) that don't affect
// FIFO lot computation.
func importedTransactionToTrade(txn *datav1.ImportedTransaction) *datav1.Trade {
	// Map imported transaction type to trade side.
	// Non-security transactions return nil — they're stored for audit/income tracking
	// but don't participate in FIFO.
	var side datav1.TradeSide
	switch txn.GetType() {
	case datav1.ImportedTransactionType_IMPORTED_TRANSACTION_TYPE_BUY,
		datav1.ImportedTransactionType_IMPORTED_TRANSACTION_TYPE_STOCK_DIVIDEND:
		// Buys and stock dividends add to position.
		side = datav1.TradeSide_TRADE_SIDE_BUY
	case datav1.ImportedTransactionType_IMPORTED_TRANSACTION_TYPE_SELL,
		datav1.ImportedTransactionType_IMPORTED_TRANSACTION_TYPE_EXPIRY,
		datav1.ImportedTransactionType_IMPORTED_TRANSACTION_TYPE_REDEMPTION:
		// Sells, expiries, and redemptions reduce position.
		side = datav1.TradeSide_TRADE_SIDE_SELL
	case datav1.ImportedTransactionType_IMPORTED_TRANSACTION_TYPE_SPLIT:
		// Splits are handled as buys or sells with zero price (quantity adjustment).
		if txn.GetQuantity() >= 0 {
			side = datav1.TradeSide_TRADE_SIDE_BUY
		} else {
			side = datav1.TradeSide_TRADE_SIDE_SELL
		}
	case datav1.ImportedTransactionType_IMPORTED_TRANSACTION_TYPE_DIVIDEND,
		datav1.ImportedTransactionType_IMPORTED_TRANSACTION_TYPE_INTEREST,
		datav1.ImportedTransactionType_IMPORTED_TRANSACTION_TYPE_FEE,
		datav1.ImportedTransactionType_IMPORTED_TRANSACTION_TYPE_WITHHOLDING_TAX,
		datav1.ImportedTransactionType_IMPORTED_TRANSACTION_TYPE_TRANSFER_IN,
		datav1.ImportedTransactionType_IMPORTED_TRANSACTION_TYPE_TRANSFER_OUT,
		datav1.ImportedTransactionType_IMPORTED_TRANSACTION_TYPE_DEPOSIT,
		datav1.ImportedTransactionType_IMPORTED_TRANSACTION_TYPE_WITHDRAWAL,
		datav1.ImportedTransactionType_IMPORTED_TRANSACTION_TYPE_OTHER,
		datav1.ImportedTransactionType_IMPORTED_TRANSACTION_TYPE_UNSPECIFIED:
		// Non-security transactions don't affect FIFO.
		return nil
	}
	// Use ibkr_symbol for FIFO matching.
	symbol := txn.GetIbkrSymbol()
	// Determine currency from price if available.
	currencyCode := "USD"
	if txn.GetPrice() != nil {
		currencyCode = txn.GetPrice().GetCurrencyCode()
	}
	// Build quantity as Decimal.
	quantity := mathpb.FromMicros(txn.GetQuantity() * 1_000_000)
	// Use price if available, otherwise zero.
	tradePrice := txn.GetPrice()
	if tradePrice == nil {
		tradePrice = moneypb.MoneyFromMicros(currencyCode, 0)
	}
	// Generate a deterministic trade ID.
	tradeID := fmt.Sprintf("imported-%s-%04d%02d%02d-%d",
		symbol,
		txn.GetDate().GetYear(), txn.GetDate().GetMonth(), txn.GetDate().GetDay(),
		txn.GetQuantity(),
	)
	return &datav1.Trade{
		TradeId:       tradeID,
		AccountId:     txn.GetAccountId(),
		TradeDate:     txn.GetDate(),
		SettleDate:    txn.GetDate(),
		Symbol:        symbol,
		AssetCategory: "STK",
		Side:          side,
		Quantity:      quantity,
		TradePrice:    tradePrice,
		Proceeds:      moneypb.MoneyFromMicros(currencyCode, 0),
		Commission:    moneypb.MoneyFromMicros(currencyCode, 0),
		CurrencyCode:  currencyCode,
	}
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
