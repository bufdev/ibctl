// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibctlfxrates provides FX rate lookups from per-pair rate files.
//
// Rates are stored in per-pair directories under the FX data directory:
// fx/{BASE}.{QUOTE}/rates.json. Each rates.json is a newline-separated
// protobuf JSON file using the ExchangeRate proto, one entry per date.
//
// The Store lazily loads rate files on first access per pair and caches
// them in memory. For holdings display, the most recent rate is used.
package ibctlfxrates

import (
	"fmt"
	"path/filepath"
	"sync"

	datav1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/data/v1"
	moneyv1 "github.com/bufdev/ibctl/internal/gen/proto/go/standard/money/v1"
	"github.com/bufdev/ibctl/internal/pkg/mathpb"
	"github.com/bufdev/ibctl/internal/pkg/moneypb"
	"github.com/bufdev/ibctl/internal/pkg/protoio"
)

// microsFactor is the number of micros per unit (6 decimal places).
const microsFactor = 1_000_000

// Store provides FX rate lookups from per-pair rate files on disk.
// Rate files are lazily loaded on first access and cached in memory.
type Store struct {
	// fxDirPath is the root FX data directory (e.g., data/v1/fx).
	fxDirPath string
	// mu protects the pairs map for concurrent lazy loading.
	mu sync.Mutex
	// pairs maps "BASE.QUOTE" to the loaded rate data for that pair.
	// Nil value means the pair was attempted but no data was found.
	pairs map[string]*pairData
}

// NewStore creates a Store that reads from the FX directory.
// Rate files are loaded lazily on first access per pair.
func NewStore(fxDirPath string) *Store {
	return &Store{
		fxDirPath: fxDirPath,
		pairs:     make(map[string]*pairData),
	}
}

// ConvertToUSD converts a Money value to USD using the most recent available rate.
// Returns the USD value as a Money proto and true if the conversion succeeded.
// Returns nil and false if the rate is not available for the currency.
// USD values are returned as-is.
func (s *Store) ConvertToUSD(money *moneyv1.Money) (*moneyv1.Money, bool) {
	if money == nil {
		return nil, false
	}
	currencyCode := money.GetCurrencyCode()
	if currencyCode == "USD" {
		return money, true
	}
	// Look up the X→USD rate for this currency.
	pair := s.loadPair(currencyCode, "USD")
	if pair == nil {
		return nil, false
	}
	rateMicros := pair.latestRateMicros
	if rateMicros == 0 {
		return nil, false
	}
	valueMicros := moneypb.MoneyToMicros(money)
	// value_usd = value * rate. Divide first to avoid int64 overflow.
	units := valueMicros / microsFactor
	remainder := valueMicros % microsFactor
	usdMicros := units*rateMicros + remainder*rateMicros/microsFactor
	return moneypb.MoneyFromMicros("USD", usdMicros), true
}

// *** PRIVATE ***

// pairData holds the loaded rate data for a single currency pair.
type pairData struct {
	// latestRateMicros is the most recent rate in micros.
	latestRateMicros int64
	// latestDate is the date string of the most recent rate.
	latestDate string
	// rates maps date strings (YYYY-MM-DD) to rate micros for date-specific lookups.
	rates map[string]int64
}

// loadPair lazily loads the rate file for a currency pair, returning the
// cached data. Returns nil if no data is available for this pair.
func (s *Store) loadPair(base string, quote string) *pairData {
	pairKey := base + "." + quote
	s.mu.Lock()
	defer s.mu.Unlock()
	// Return cached data if already loaded (even if nil = no data found).
	if pair, loaded := s.pairs[pairKey]; loaded {
		return pair
	}
	// Load the rates file for this pair from disk.
	ratesPath := filepath.Join(s.fxDirPath, pairKey, "rates.json")
	rates, err := protoio.ReadMessagesJSON(ratesPath, func() *datav1.ExchangeRate { return &datav1.ExchangeRate{} })
	if err != nil || len(rates) == 0 {
		// No data for this pair — cache the nil result to avoid repeated disk reads.
		s.pairs[pairKey] = nil
		return nil
	}
	// Build the pair data with date-indexed rates and track the most recent.
	pair := &pairData{
		rates: make(map[string]int64, len(rates)),
	}
	for _, rate := range rates {
		dateStr := fmt.Sprintf("%04d-%02d-%02d", rate.GetDate().GetYear(), rate.GetDate().GetMonth(), rate.GetDate().GetDay())
		rateMicros := mathpb.ToMicros(rate.GetRate())
		pair.rates[dateStr] = rateMicros
		// Track the most recent rate for "latest" lookups.
		if pair.latestDate == "" || dateStr > pair.latestDate {
			pair.latestRateMicros = rateMicros
			pair.latestDate = dateStr
		}
	}
	s.pairs[pairKey] = pair
	return pair
}
