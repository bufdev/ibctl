// Copyright 2026 Peter Edge
//
// All rights reserved.

package ibkractivitycsv

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseFile(t *testing.T) {
	t.Parallel()
	// Parse the synthetic Activity Statement CSV.
	statement, err := ParseFile("testdata/sample.csv")
	require.NoError(t, err)

	// Verify stock trades were parsed (only Order rows).
	require.Len(t, statement.Trades, 4, "expected 4 stock trades")
	// First stock trade should be AAPL buy.
	firstTrade := statement.Trades[0]
	require.Equal(t, "AAPL", firstTrade.Symbol)
	require.Equal(t, "USD", firstTrade.CurrencyCode)
	require.Equal(t, "100", firstTrade.Quantity)
	require.Equal(t, "150.50", firstTrade.TradePrice)
	require.Equal(t, "2026-01-02", firstTrade.DateTime.Format("2006-01-02"))
	// Third trade should be MSFT sell (negative quantity).
	msftTrade := statement.Trades[2]
	require.Equal(t, "MSFT", msftTrade.Symbol)
	require.Equal(t, "-25", msftTrade.Quantity)

	// Verify forex trades were parsed.
	require.Len(t, statement.ForexTrades, 1, "expected 1 forex trade")
	require.Equal(t, "USD.CAD", statement.ForexTrades[0].Symbol)
	require.Equal(t, "17000", statement.ForexTrades[0].Quantity)

	// Verify positions were parsed (only Summary rows).
	require.Len(t, statement.Positions, 3, "expected 3 positions")
	require.Equal(t, "AAPL", statement.Positions[0].Symbol)

	// Verify dividends were parsed (excluding Total rows).
	require.Len(t, statement.Dividends, 2, "expected 2 dividends")
	require.Equal(t, "25.00", statement.Dividends[0].Amount)

	// Verify withholding taxes were parsed.
	require.Len(t, statement.WithholdingTaxes, 1, "expected 1 withholding tax")
	require.Equal(t, "-1.50", statement.WithholdingTaxes[0].Amount)

	// Verify interest items were parsed.
	require.Len(t, statement.InterestItems, 2, "expected 2 interest items")

	// Verify instrument info was parsed (both stocks and bonds).
	require.Len(t, statement.InstrumentInfos, 5, "expected 5 instrument infos (4 stocks + 1 bond)")
	// Check stock instrument info.
	var foundStock, foundBond bool
	for _, info := range statement.InstrumentInfos {
		if info.Symbol == "AAPL" {
			foundStock = true
			require.Equal(t, "APPLE INC", info.Description)
			require.Equal(t, "COMMON", info.InstrumentType)
			require.Equal(t, "NASDAQ", info.ListingExchange)
			require.Equal(t, "265598", info.Conid)
			require.Equal(t, "Stocks", info.AssetCategory)
		}
		if info.Symbol == "TEST 2.5 01/15/28" {
			foundBond = true
			require.Equal(t, "Corp", info.InstrumentType)
			require.Equal(t, "Bonds", info.AssetCategory)
			require.Contains(t, info.Issuer, "Test Corp")
			require.Equal(t, "2028-01-15", info.Maturity)
		}
	}
	require.True(t, foundStock, "expected AAPL stock instrument info")
	require.True(t, foundBond, "expected TEST bond instrument info")
}
