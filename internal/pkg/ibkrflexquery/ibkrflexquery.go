// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibkrflexquery provides an API client for the IBKR Flex Query Web Service.
//
// The Flex Query Web Service is a two-step REST API:
//  1. SendRequest: Submits a query and returns a reference code.
//  2. GetStatement: Polls with the reference code until the XML statement is ready.
//
// Both endpoints require a Flex Web Service token for authentication and
// a "Java" User-Agent header. The GetStatement endpoint may return a "not ready"
// response (error code 1019), in which case the client retries with a delay.
//
// The returned FlexStatement contains three sections: Trades, OpenPositions,
// and CashTransactions, each parsed from the IBKR XML attribute-based format.
package ibkrflexquery

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bufdev/ibctl/internal/standard/xtime"
)

const (
	// sendRequestURL is the IBKR Flex Web Service endpoint for initiating a query.
	sendRequestURL = "https://ndcdyn.interactivebrokers.com/AccountManagement/FlexWebService/SendRequest"
	// getStatementURL is the IBKR Flex Web Service endpoint for retrieving a statement.
	getStatementURL = "https://ndcdyn.interactivebrokers.com/AccountManagement/FlexWebService/GetStatement"
	// userAgent is the required User-Agent header for IBKR (IBKR expects "Java").
	userAgent = "Java"
	// maxRetries is the maximum number of polling attempts for GetStatement.
	maxRetries = 10
	// retryDelay is the delay between GetStatement polling attempts.
	retryDelay = 5 * time.Second
)

// Client is the interface for downloading Flex Query data from IBKR.
type Client interface {
	// Download fetches and parses a Flex Query statement.
	//
	// The token is the Flex Web Service token generated in the IBKR portal.
	// The queryID identifies which Flex Query to execute.
	// The fromDate and toDate optionally override the query's configured period.
	// Pass zero-value dates to use the query's default period.
	// If one is set, both must be set. Each request is limited to 365 days.
	//
	// The method performs the two-step API flow (SendRequest → GetStatement),
	// parses the XML response, and returns the structured statement data.
	Download(ctx context.Context, token string, queryID string, fromDate xtime.Date, toDate xtime.Date) (*FlexStatement, error)
}

// NewClient creates a new Flex Query API client. The logger is required.
func NewClient(logger *slog.Logger) Client {
	return &client{
		httpClient: http.DefaultClient,
		logger:     logger,
	}
}

// FlexStatement contains the trades, positions, and cash transactions
// returned by a Flex Query.
type FlexStatement struct {
	// Trades is the list of trade executions.
	Trades []XMLTrade `xml:"Trades>Trade"`
	// OpenPositions is the list of currently open positions.
	OpenPositions []XMLPosition `xml:"OpenPositions>OpenPosition"`
	// CashTransactions is the list of cash transactions (used for FX rate extraction).
	CashTransactions []XMLCashTransaction `xml:"CashTransactions>CashTransaction"`
}

// XMLTrade represents a trade in the IBKR Flex Query XML format.
// All fields are XML attributes.
type XMLTrade struct {
	TradeID          string `xml:"tradeID,attr"`
	TradeDate        string `xml:"tradeDate,attr"`
	SettleDateTarget string `xml:"settleDateTarget,attr"`
	Symbol           string `xml:"symbol,attr"`
	Description      string `xml:"description,attr"`
	AssetCategory    string `xml:"assetCategory,attr"`
	BuySell          string `xml:"buySell,attr"`
	Quantity         string `xml:"quantity,attr"`
	TradePrice       string `xml:"tradePrice,attr"`
	Proceeds         string `xml:"proceeds,attr"`
	IBCommission     string `xml:"ibCommission,attr"`
	Currency         string `xml:"currency,attr"`
	FifoPnlRealized  string `xml:"fifoPnlRealized,attr"`
}

// XMLPosition represents an open position in the IBKR Flex Query XML format.
// All fields are XML attributes.
type XMLPosition struct {
	Symbol            string `xml:"symbol,attr"`
	Description       string `xml:"description,attr"`
	AssetCategory     string `xml:"assetCategory,attr"`
	Quantity          string `xml:"quantity,attr"`
	CostBasisPrice    string `xml:"costBasisPrice,attr"`
	MarkPrice         string `xml:"markPrice,attr"`
	PositionValue     string `xml:"positionValue,attr"`
	FifoPnlUnrealized string `xml:"fifoPnlUnrealized,attr"`
	Currency          string `xml:"currency,attr"`
}

// XMLCashTransaction represents a cash transaction in the IBKR Flex Query XML format.
// Used primarily for extracting FX rates.
type XMLCashTransaction struct {
	DateTime     string `xml:"dateTime,attr"`
	Currency     string `xml:"currency,attr"`
	FxRateToBase string `xml:"fxRateToBase,attr"`
	Type         string `xml:"type,attr"`
	Amount       string `xml:"amount,attr"`
	Description  string `xml:"description,attr"`
}

// *** PRIVATE ***

type client struct {
	httpClient *http.Client
	logger     *slog.Logger
}

// flexQueryResponse is the top-level XML structure of a Flex Query statement.
type flexQueryResponse struct {
	XMLName        xml.Name       `xml:"FlexQueryResponse"`
	FlexStatements flexStatements `xml:"FlexStatements"`
}

// flexStatements contains one or more FlexStatement elements.
type flexStatements struct {
	FlexStatement FlexStatement `xml:"FlexStatement"`
}

// sendResponse is the XML response from the SendRequest endpoint.
type sendResponse struct {
	XMLName       xml.Name `xml:"FlexStatementResponse"`
	Status        string   `xml:"Status"`
	ReferenceCode string   `xml:"ReferenceCode"`
	URL           string   `xml:"Url"`
	ErrorCode     string   `xml:"ErrorCode"`
	ErrorMessage  string   `xml:"ErrorMessage"`
}

func (c *client) Download(ctx context.Context, token string, queryID string, fromDate xtime.Date, toDate xtime.Date) (*FlexStatement, error) {
	// Validate required parameters.
	if token == "" {
		return nil, errors.New("token is required")
	}
	if queryID == "" {
		return nil, errors.New("query ID is required")
	}
	// Validate date parameters: if one is set, both must be set.
	if fromDate.IsZero() != toDate.IsZero() {
		return nil, errors.New("fromDate and toDate must both be set or both be zero")
	}
	// Step 1: Send the request to get a reference code.
	referenceCode, err := c.sendRequest(ctx, token, queryID, fromDate, toDate)
	if err != nil {
		return nil, fmt.Errorf("sending flex query request: %w", err)
	}
	c.logger.Info("flex query request sent", "reference_code", referenceCode)
	// Step 2: Poll for the statement XML using the reference code.
	xmlData, err := c.getStatement(ctx, token, referenceCode)
	if err != nil {
		return nil, fmt.Errorf("getting flex query statement: %w", err)
	}
	// Step 3: Parse the XML response into structured data.
	response, err := parseFlexQueryResponse(xmlData)
	if err != nil {
		return nil, fmt.Errorf("parsing flex query response: %w", err)
	}
	return &response.FlexStatements.FlexStatement, nil
}

// sendRequest initiates a Flex Query and returns the reference code.
func (c *client) sendRequest(ctx context.Context, token string, queryID string, fromDate xtime.Date, toDate xtime.Date) (string, error) {
	// Build the request URL with query parameters.
	reqURL := fmt.Sprintf("%s?v=3&t=%s&q=%s", sendRequestURL, token, queryID)
	// Optionally append date range override parameters (IBKR expects YYYYMMDD format).
	if !fromDate.IsZero() && !toDate.IsZero() {
		reqURL += fmt.Sprintf("&fd=%04d%02d%02d&td=%04d%02d%02d", fromDate.Year, fromDate.Month, fromDate.Day, toDate.Year, toDate.Month, toDate.Day)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	// IBKR requires the "Java" User-Agent header.
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}
	var sendResp sendResponse
	if err := xml.Unmarshal(body, &sendResp); err != nil {
		return "", fmt.Errorf("parsing send response: %w", err)
	}
	if sendResp.Status != "Success" {
		return "", fmt.Errorf("flex query request failed: %s (code: %s)", sendResp.ErrorMessage, sendResp.ErrorCode)
	}
	return sendResp.ReferenceCode, nil
}

// getStatement polls the GetStatement endpoint until the data is ready.
func (c *client) getStatement(ctx context.Context, token string, referenceCode string) ([]byte, error) {
	for attempt := range maxRetries {
		if attempt > 0 {
			c.logger.Info("waiting for flex query statement", "attempt", attempt+1)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryDelay):
			}
		}
		// Build the request URL with query parameters.
		reqURL := fmt.Sprintf("%s?v=3&t=%s&q=%s", getStatementURL, token, referenceCode)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		// IBKR requires the "Java" User-Agent header.
		req.Header.Set("User-Agent", userAgent)
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
		}
		// Check if the response is an XML error response (statement not ready yet).
		bodyStr := strings.TrimSpace(string(body))
		if strings.HasPrefix(bodyStr, "<FlexStatementResponse") {
			var getResp sendResponse
			if err := xml.Unmarshal(body, &getResp); err != nil {
				return nil, fmt.Errorf("parsing get response: %w", err)
			}
			// Error code 1019 means the statement is being generated, retry.
			if getResp.ErrorCode == "1019" {
				continue
			}
			return nil, fmt.Errorf("flex query statement failed: %s (code: %s)", getResp.ErrorMessage, getResp.ErrorCode)
		}
		// If it's not an error response, it's the actual statement XML.
		return body, nil
	}
	return nil, fmt.Errorf("flex query statement not ready after %d attempts", maxRetries)
}

// parseFlexQueryResponse parses the raw XML data into a flexQueryResponse.
func parseFlexQueryResponse(data []byte) (*flexQueryResponse, error) {
	var response flexQueryResponse
	if err := xml.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	return &response, nil
}
