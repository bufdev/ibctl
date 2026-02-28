// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package flexquery provides an API client for the IBKR Flex Query Web Service.
package flexquery

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
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
	// Download fetches the Flex Query XML data using the given token and query ID.
	Download(ctx context.Context, token string, queryID string) ([]byte, error)
}

// ClientOption is a functional option for configuring the Client.
type ClientOption func(*client)

// ClientWithHTTPClient sets the HTTP client to use for requests.
func ClientWithHTTPClient(httpClient *http.Client) ClientOption {
	return func(c *client) {
		c.httpClient = httpClient
	}
}

// ClientWithLogger sets the logger for the client.
func ClientWithLogger(logger *slog.Logger) ClientOption {
	return func(c *client) {
		c.logger = logger
	}
}

// NewClient creates a new Flex Query API client with the given options.
func NewClient(options ...ClientOption) Client {
	c := &client{
		httpClient: http.DefaultClient,
		logger:     slog.Default(),
	}
	for _, option := range options {
		option(c)
	}
	return c
}

type client struct {
	httpClient *http.Client
	logger     *slog.Logger
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

func (c *client) Download(ctx context.Context, token string, queryID string) ([]byte, error) {
	// Step 1: Send the request to get a reference code.
	referenceCode, err := c.sendRequest(ctx, token, queryID)
	if err != nil {
		return nil, fmt.Errorf("sending flex query request: %w", err)
	}
	c.logger.Info("flex query request sent", "reference_code", referenceCode)
	// Step 2: Poll for the statement using the reference code.
	data, err := c.getStatement(ctx, token, referenceCode)
	if err != nil {
		return nil, fmt.Errorf("getting flex query statement: %w", err)
	}
	return data, nil
}

// sendRequest initiates a Flex Query and returns the reference code.
func (c *client) sendRequest(ctx context.Context, token string, queryID string) (string, error) {
	// Build the request URL with query parameters.
	reqURL := fmt.Sprintf("%s?v=3&t=%s&q=%s", sendRequestURL, token, queryID)
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
