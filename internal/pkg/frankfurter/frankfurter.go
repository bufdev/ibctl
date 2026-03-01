// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package frankfurter provides a client for fetching exchange rates from frankfurter.dev.
//
// The frankfurter.dev API is free and does not require an API key or authentication.
// See https://frankfurter.dev for usage details and rate limits.
package frankfurter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	mathv1 "github.com/bufdev/ibctl/internal/gen/proto/go/standard/math/v1"
	"github.com/bufdev/ibctl/internal/pkg/mathpb"
)

// baseURL is the frankfurter.dev API base URL.
const baseURL = "https://api.frankfurter.dev/v1"

// DailyRate is a single daily exchange rate returned by the API.
type DailyRate struct {
	// Date is the rate date in YYYY-MM-DD format.
	Date string
	// Rate is the exchange rate value as a Decimal.
	Rate *mathv1.Decimal
}

// Client is the interface for fetching exchange rates.
type Client interface {
	// GetRates fetches daily exchange rates for a date range.
	// Returns rates as structured DailyRate values.
	GetRates(ctx context.Context, baseCurrency string, quoteCurrency string, startDate string, endDate string) ([]DailyRate, error)
}

// NewClient creates a new exchange rate client.
func NewClient() Client {
	return &client{
		httpClient: http.DefaultClient,
	}
}

type client struct {
	httpClient *http.Client
}

func (c *client) GetRates(ctx context.Context, baseCurrency string, quoteCurrency string, startDate string, endDate string) ([]DailyRate, error) {
	// Build the request URL for the time series endpoint.
	reqURL := fmt.Sprintf("%s/%s..%s?base=%s&symbols=%s", baseURL, startDate, endDate, baseCurrency, quoteCurrency)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}
	var frankfurterResp frankfurterResponse
	if err := json.Unmarshal(body, &frankfurterResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	// Convert the nested map to structured DailyRate values.
	var rates []DailyRate
	for date, rateMap := range frankfurterResp.Rates {
		if rateFloat, ok := rateMap[quoteCurrency]; ok {
			// Convert the float64 rate to a Decimal proto.
			rateStr := fmt.Sprintf("%f", rateFloat)
			rateDecimal, err := mathpb.NewDecimal(rateStr)
			if err != nil {
				continue
			}
			rates = append(rates, DailyRate{
				Date: date,
				Rate: rateDecimal,
			})
		}
	}
	return rates, nil
}

// *** PRIVATE ***

// frankfurterResponse is the JSON response from the frankfurter.dev API for time series.
type frankfurterResponse struct {
	Rates map[string]map[string]float64 `json:"rates"`
}
