// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package bankofcanada provides a client for fetching FX rates from the
// Bank of Canada valet API.
//
// The valet API provides daily noon exchange rates for currency pairs quoted
// in CAD. Series names follow the pattern FX{base}CAD (e.g., FXUSDCAD for
// USD→CAD). The API is free and does not require authentication.
//
// See https://www.bankofcanada.ca/valet/docs for API documentation.
package bankofcanada

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	mathv1 "github.com/bufdev/ibctl/internal/gen/proto/go/standard/math/v1"
	"github.com/bufdev/ibctl/internal/pkg/mathpb"
)

// baseURL is the Bank of Canada valet API base URL.
const baseURL = "https://www.bankofcanada.ca/valet/observations"

// DailyRate is a single daily exchange rate returned by the API.
type DailyRate struct {
	// Date is the rate date in YYYY-MM-DD format.
	Date string
	// Rate is the exchange rate value as a Decimal (e.g., 1.3685 for USD→CAD).
	Rate *mathv1.Decimal
}

// Client fetches daily FX rates from the Bank of Canada valet API.
type Client interface {
	// GetRates returns daily X→CAD rates for a date range.
	// The baseCurrency is the 3-letter ISO currency code (e.g., "USD", "GBP").
	// Returns rates as structured DailyRate values.
	GetRates(ctx context.Context, baseCurrency string, startDate string, endDate string) ([]DailyRate, error)
}

// NewClient creates a new Bank of Canada API client.
func NewClient() Client {
	return &client{
		httpClient: http.DefaultClient,
	}
}

type client struct {
	httpClient *http.Client
}

func (c *client) GetRates(ctx context.Context, baseCurrency string, startDate string, endDate string) ([]DailyRate, error) {
	// Build the series name (e.g., FXUSDCAD) and request URL.
	seriesName := fmt.Sprintf("FX%sCAD", baseCurrency)
	reqURL := fmt.Sprintf("%s/%s/json?start_date=%s&end_date=%s", baseURL, seriesName, startDate, endDate)
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
	var valetResp valetResponse
	if err := json.Unmarshal(body, &valetResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	// Extract structured DailyRate values from the observations array.
	var rates []DailyRate
	for _, obs := range valetResp.Observations {
		rateValue, ok := obs.Rates[seriesName]
		if !ok {
			continue
		}
		// Skip observations with no rate value (e.g., holidays).
		if rateValue.Value == "" {
			continue
		}
		// Parse the rate string into a Decimal proto.
		rateDecimal, err := mathpb.NewDecimal(rateValue.Value)
		if err != nil {
			continue
		}
		rates = append(rates, DailyRate{
			Date: obs.Date,
			Rate: rateDecimal,
		})
	}
	return rates, nil
}

// *** PRIVATE ***

// valetResponse is the JSON response from the Bank of Canada valet API.
type valetResponse struct {
	// Observations is the array of daily rate observations.
	Observations []valetObservation `json:"observations"`
}

// valetObservation is a single daily observation from the valet API.
// The rate is keyed by the series name (e.g., "FXUSDCAD").
type valetObservation struct {
	// Date is the observation date (YYYY-MM-DD).
	Date string `json:"d"`
	// Rates maps series names to their values. The key is the series name
	// (e.g., "FXUSDCAD") and the value contains the rate.
	Rates map[string]valetRateValue `json:"-"`
}

// valetRateValue is the rate value within an observation.
type valetRateValue struct {
	// Value is the exchange rate as a string (e.g., "1.3685").
	Value string `json:"v"`
}

// UnmarshalJSON implements custom JSON unmarshaling for valetObservation.
// The observation is a flat object with "d" for date and series-named keys
// (e.g., "FXUSDCAD": {"v": "1.3685"}) for rates. We parse "d" as the date
// and all other keys as rate values.
func (o *valetObservation) UnmarshalJSON(data []byte) error {
	// First pass: unmarshal into a raw map to extract all keys.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	// Extract the date field.
	if dateRaw, ok := raw["d"]; ok {
		if err := json.Unmarshal(dateRaw, &o.Date); err != nil {
			return fmt.Errorf("parsing date: %w", err)
		}
	}
	// All other keys are rate series (e.g., "FXUSDCAD").
	o.Rates = make(map[string]valetRateValue)
	for key, val := range raw {
		if key == "d" {
			continue
		}
		var rv valetRateValue
		if err := json.Unmarshal(val, &rv); err != nil {
			// Skip non-rate fields that don't match the expected structure.
			continue
		}
		o.Rates[key] = rv
	}
	return nil
}
