// Copyright 2026 Peter Edge
//
// All rights reserved.

package flexquery

import "encoding/xml"

// FlexQueryResponse is the top-level XML structure of a Flex Query statement.
type FlexQueryResponse struct {
	XMLName        xml.Name       `xml:"FlexQueryResponse"`
	FlexStatements FlexStatements `xml:"FlexStatements"`
}

// FlexStatements contains one or more FlexStatement elements.
type FlexStatements struct {
	FlexStatement FlexStatement `xml:"FlexStatement"`
}

// FlexStatement contains the trades, positions, and cash transactions.
type FlexStatement struct {
	Trades           []XMLTrade           `xml:"Trades>Trade"`
	OpenPositions    []XMLPosition        `xml:"OpenPositions>OpenPosition"`
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

// ParseFlexQueryResponse parses the raw XML data into a FlexQueryResponse.
func ParseFlexQueryResponse(data []byte) (*FlexQueryResponse, error) {
	var response FlexQueryResponse
	if err := xml.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	return &response, nil
}
