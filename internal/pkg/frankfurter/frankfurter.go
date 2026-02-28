// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package frankfurter provides a client for fetching exchange rates from frankfurter.dev.
package frankfurter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// baseURL is the frankfurter.dev API base URL.
const baseURL = "https://api.frankfurter.dev/v1"

// Client is the interface for fetching exchange rates.
//
// The frankfurter.dev API is free and does not require an API key or authentication.
// See https://frankfurter.dev for usage details and rate limits.
type Client interface {
	// GetRates fetches exchange rates for a date range.
	// Returns a map of date strings (YYYY-MM-DD) to rate strings.
	GetRates(ctx context.Context, baseCurrency string, quoteCurrency string, startDate string, endDate string) (map[string]string, error)
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

// frankfurterResponse is the JSON response from the frankfurter.dev API for time series.
type frankfurterResponse struct {
	Rates map[string]map[string]float64 `json:"rates"`
}

func (c *client) GetRates(ctx context.Context, baseCurrency string, quoteCurrency string, startDate string, endDate string) (map[string]string, error) {
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
	// Convert the nested map to a flat date->rate map.
	result := make(map[string]string, len(frankfurterResp.Rates))
	for date, rates := range frankfurterResp.Rates {
		if rate, ok := rates[quoteCurrency]; ok {
			result[date] = fmt.Sprintf("%f", rate)
		}
	}
	return result, nil
}
