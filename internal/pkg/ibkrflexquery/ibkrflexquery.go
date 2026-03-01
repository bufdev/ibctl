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
// a "Java" User-Agent header. Both endpoints may return transient errors
// (e.g., 1001 server busy, 1019 statement generating) which are retried
// with exponential backoff.
//
// The response contains one FlexStatement per IBKR account. Each statement
// includes Trades, OpenPositions, CashTransactions, Transfers, TradeTransfers,
// and CorporateActions sections, parsed from the IBKR XML attribute-based format.
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

	"github.com/bufdev/ibctl/internal/pkg/backoff"
	"github.com/bufdev/ibctl/internal/standard/xtime"
)

const (
	// sendRequestURL is the IBKR Flex Web Service endpoint for initiating a query.
	sendRequestURL = "https://ndcdyn.interactivebrokers.com/AccountManagement/FlexWebService/SendRequest"
	// getStatementURL is the IBKR Flex Web Service endpoint for retrieving a statement.
	getStatementURL = "https://ndcdyn.interactivebrokers.com/AccountManagement/FlexWebService/GetStatement"
	// userAgent is the required User-Agent header for IBKR (IBKR expects "Java").
	userAgent = "Java"
	// maxAttempts is the maximum number of attempts for each API call.
	maxAttempts = 10
	// initialRetryDelay is the initial delay before the first retry.
	initialRetryDelay = 2 * time.Second
	// maxRetryDelay is the maximum delay between retries.
	maxRetryDelay = 30 * time.Second
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
	// parses the XML response, and returns one FlexStatement per IBKR account.
	Download(ctx context.Context, token string, queryID string, fromDate xtime.Date, toDate xtime.Date) ([]FlexStatement, error)
}

// NewClient creates a new Flex Query API client. The logger is required.
func NewClient(logger *slog.Logger) Client {
	return &client{
		httpClient: http.DefaultClient,
		logger:     logger,
	}
}

// FlexStatement contains the data returned by a Flex Query for a single IBKR account.
type FlexStatement struct {
	// AccountId is the IBKR account identifier (e.g., "U1234567").
	AccountId string `xml:"accountId,attr"`
	// Trades is the list of trade executions.
	Trades []XMLTrade `xml:"Trades>Trade"`
	// OpenPositions is the list of currently open positions.
	OpenPositions []XMLPosition `xml:"OpenPositions>OpenPosition"`
	// CashTransactions is the list of cash transactions (used for FX rate extraction).
	CashTransactions []XMLCashTransaction `xml:"CashTransactions>CashTransaction"`
	// Transfers is the list of position transfers (ACATS, ATON, FOP, internal).
	Transfers []XMLTransfer `xml:"Transfers>Transfer"`
	// TradeTransfers is the list of transferred trade cost basis records.
	TradeTransfers []XMLTradeTransfer `xml:"TradeTransfers>TradeTransfer"`
	// CorporateActions is the list of corporate action events.
	CorporateActions []XMLCorporateAction `xml:"CorporateActions>CorporateAction"`
	// CashReport is the cash balance report by currency.
	CashReport []XMLCashReportCurrency `xml:"CashReport>CashReportCurrency"`
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
// All fields are XML attributes. Note: IBKR uses "position" (not "quantity") for the held amount.
type XMLPosition struct {
	Symbol        string `xml:"symbol,attr"`
	Description   string `xml:"description,attr"`
	AssetCategory string `xml:"assetCategory,attr"`
	// Position is the quantity held (IBKR uses the attribute name "position", not "quantity").
	Position          string `xml:"position,attr"`
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

// XMLTransfer represents a position transfer in the IBKR Flex Query XML format.
// Captures ACATS, ATON, FOP, and internal transfers.
type XMLTransfer struct {
	Type          string `xml:"type,attr"`
	Direction     string `xml:"direction,attr"`
	Symbol        string `xml:"symbol,attr"`
	DateTime      string `xml:"dateTime,attr"`
	Quantity      string `xml:"quantity,attr"`
	TransferPrice string `xml:"transferPrice,attr"`
	Currency      string `xml:"currency,attr"`
	Description   string `xml:"description,attr"`
	AssetCategory string `xml:"assetCategory,attr"`
}

// XMLTradeTransfer represents a transferred trade cost basis in the IBKR Flex Query XML format.
// Used to preserve holding period and cost basis for positions transferred from another broker.
type XMLTradeTransfer struct {
	Symbol                string `xml:"symbol,attr"`
	DateTime              string `xml:"dateTime,attr"`
	Quantity              string `xml:"quantity,attr"`
	OrigTradePrice        string `xml:"origTradePrice,attr"`
	OrigTradeDate         string `xml:"origTradeDate,attr"`
	OrigTradeID           string `xml:"origTradeID,attr"`
	Cost                  string `xml:"cost,attr"`
	HoldingPeriodDateTime string `xml:"holdingPeriodDateTime,attr"`
	Currency              string `xml:"currency,attr"`
	AssetCategory         string `xml:"assetCategory,attr"`
}

// XMLCorporateAction represents a corporate action in the IBKR Flex Query XML format.
// Covers stock splits, reverse splits, mergers, spinoffs, and other events.
type XMLCorporateAction struct {
	Type              string `xml:"type,attr"`
	Symbol            string `xml:"symbol,attr"`
	DateTime          string `xml:"dateTime,attr"`
	Quantity          string `xml:"quantity,attr"`
	Amount            string `xml:"amount,attr"`
	Currency          string `xml:"currency,attr"`
	ActionDescription string `xml:"actionDescription,attr"`
	AssetCategory     string `xml:"assetCategory,attr"`
}

// XMLCashReportCurrency represents a cash balance for a single currency
// from the IBKR Flex Query Cash Report section.
type XMLCashReportCurrency struct {
	// Currency is the ISO currency code (e.g., "USD", "CAD").
	Currency string `xml:"currency,attr"`
	// EndingCash is the total cash balance including unsettled trades.
	EndingCash string `xml:"endingCash,attr"`
	// EndingSettledCash is the settled cash balance (actual available funds).
	EndingSettledCash string `xml:"endingSettledCash,attr"`
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

// flexStatements contains one FlexStatement per IBKR account.
type flexStatements struct {
	// Statements is the list of per-account statements.
	Statements []FlexStatement `xml:"FlexStatement"`
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

// retryableErrorCodes are IBKR error codes that indicate a transient failure.
var retryableErrorCodes = map[string]bool{
	"1001": true, // Statement could not be generated at this time.
	"1019": true, // Statement is being generated, please try again shortly.
}

func (c *client) Download(ctx context.Context, token string, queryID string, fromDate xtime.Date, toDate xtime.Date) ([]FlexStatement, error) {
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
	// Step 1: Send the request to get a reference code, with backoff on transient errors.
	referenceCode, err := c.sendRequest(ctx, token, queryID, fromDate, toDate)
	if err != nil {
		return nil, fmt.Errorf("sending flex query request: %w", err)
	}
	c.logger.Info("flex query request sent", "reference_code", referenceCode)
	// Step 2: Poll for the statement XML using the reference code, with backoff.
	xmlData, err := c.getStatement(ctx, token, referenceCode)
	if err != nil {
		return nil, fmt.Errorf("getting flex query statement: %w", err)
	}
	// Step 3: Parse the XML response into structured data.
	response, err := parseFlexQueryResponse(xmlData)
	if err != nil {
		return nil, fmt.Errorf("parsing flex query response: %w", err)
	}
	// Return all per-account statements.
	return response.FlexStatements.Statements, nil
}

// sendRequest initiates a Flex Query and returns the reference code.
// Retries on transient IBKR errors with exponential backoff.
func (c *client) sendRequest(ctx context.Context, token string, queryID string, fromDate xtime.Date, toDate xtime.Date) (string, error) {
	// Build the request URL with query parameters.
	// Parameter order matches IBKR docs: t, q, [fd, td], v.
	reqURL := fmt.Sprintf("%s?t=%s&q=%s", sendRequestURL, token, queryID)
	// Optionally append date range override parameters (IBKR expects YYYYMMDD format).
	if !fromDate.IsZero() && !toDate.IsZero() {
		reqURL += fmt.Sprintf("&fd=%04d%02d%02d&td=%04d%02d%02d", fromDate.Year, fromDate.Month, fromDate.Day, toDate.Year, toDate.Month, toDate.Day)
	}
	reqURL += "&v=3"
	return backoff.Retry(ctx, maxAttempts, initialRetryDelay, maxRetryDelay,
		func(ctx context.Context, attempt int) (string, bool, error) {
			if attempt > 0 {
				c.logger.Info("retrying send request", "attempt", attempt+1)
			}
			c.logger.Debug("send request", "query_id", queryID, "has_dates", !fromDate.IsZero())
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
			if err != nil {
				return "", false, err
			}
			// IBKR requires the "Java" User-Agent header.
			req.Header.Set("User-Agent", userAgent)
			resp, err := c.httpClient.Do(req)
			if err != nil {
				return "", false, err
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return "", false, err
			}
			if resp.StatusCode != http.StatusOK {
				return "", false, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
			}
			var sendResp sendResponse
			if err := xml.Unmarshal(body, &sendResp); err != nil {
				return "", false, fmt.Errorf("parsing send response: %w", err)
			}
			if sendResp.Status != "Success" {
				retryable := retryableErrorCodes[sendResp.ErrorCode]
				if retryable {
					c.logger.Warn("transient IBKR error, will retry", "code", sendResp.ErrorCode, "message", sendResp.ErrorMessage)
				}
				return "", retryable, fmt.Errorf("%s (code: %s)", sendResp.ErrorMessage, sendResp.ErrorCode)
			}
			return sendResp.ReferenceCode, false, nil
		},
	)
}

// getStatement polls the GetStatement endpoint until the data is ready.
// Retries on transient IBKR errors with exponential backoff.
func (c *client) getStatement(ctx context.Context, token string, referenceCode string) ([]byte, error) {
	return backoff.Retry(ctx, maxAttempts, initialRetryDelay, maxRetryDelay,
		func(ctx context.Context, attempt int) ([]byte, bool, error) {
			if attempt > 0 {
				c.logger.Info("waiting for flex query statement", "attempt", attempt+1)
			}
			// Build the request URL with query parameters.
			// Parameter order matches IBKR docs: t, q, v.
			reqURL := fmt.Sprintf("%s?t=%s&q=%s&v=3", getStatementURL, token, referenceCode)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
			if err != nil {
				return nil, false, err
			}
			// IBKR requires the "Java" User-Agent header.
			req.Header.Set("User-Agent", userAgent)
			resp, err := c.httpClient.Do(req)
			if err != nil {
				return nil, false, err
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return nil, false, err
			}
			if resp.StatusCode != http.StatusOK {
				return nil, false, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
			}
			// Check if the response is an XML error response (statement not ready yet).
			bodyStr := strings.TrimSpace(string(body))
			if strings.HasPrefix(bodyStr, "<FlexStatementResponse") {
				var getResp sendResponse
				if err := xml.Unmarshal(body, &getResp); err != nil {
					return nil, false, fmt.Errorf("parsing get response: %w", err)
				}
				retryable := retryableErrorCodes[getResp.ErrorCode]
				if retryable {
					c.logger.Warn("transient IBKR error, will retry", "code", getResp.ErrorCode, "message", getResp.ErrorMessage)
				}
				return nil, retryable, fmt.Errorf("%s (code: %s)", getResp.ErrorMessage, getResp.ErrorCode)
			}
			// If it's not an error response, it's the actual statement XML.
			return body, false, nil
		},
	)
}

// parseFlexQueryResponse parses the raw XML data into a flexQueryResponse.
func parseFlexQueryResponse(data []byte) (*flexQueryResponse, error) {
	var response flexQueryResponse
	if err := xml.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	return &response, nil
}
