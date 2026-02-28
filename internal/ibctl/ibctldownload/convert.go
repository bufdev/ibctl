// Copyright 2026 Peter Edge
//
// All rights reserved.

package ibctldownload

import (
	"fmt"
	"strconv"
	"time"

	positionv1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/position/v1"
	tradev1 "github.com/bufdev/ibctl/internal/gen/proto/go/ibctl/trade/v1"
	"github.com/bufdev/ibctl/internal/pkg/flexquery"
	"github.com/bufdev/ibctl/internal/pkg/moneypb"
	"github.com/bufdev/ibctl/internal/pkg/timepb"
)

// xmlTradeToProto converts an XML trade from the Flex Query response to a proto Trade.
func xmlTradeToProto(xmlTrade *flexquery.XMLTrade) (*tradev1.Trade, error) {
	// Parse the trade date (format: YYYYMMDD).
	tradeDate, err := parseIBKRDate(xmlTrade.TradeDate)
	if err != nil {
		return nil, fmt.Errorf("parsing trade date %q: %w", xmlTrade.TradeDate, err)
	}
	protoTradeDate, err := timepb.NewProtoDate(tradeDate.Year(), tradeDate.Month(), tradeDate.Day())
	if err != nil {
		return nil, err
	}
	// Parse the settle date (format: YYYYMMDD).
	settleDate, err := parseIBKRDate(xmlTrade.SettleDateTarget)
	if err != nil {
		return nil, fmt.Errorf("parsing settle date %q: %w", xmlTrade.SettleDateTarget, err)
	}
	protoSettleDate, err := timepb.NewProtoDate(settleDate.Year(), settleDate.Month(), settleDate.Day())
	if err != nil {
		return nil, err
	}
	// Parse numeric fields using the Money proto helper.
	currency := xmlTrade.Currency
	tradePrice, err := moneypb.NewProtoMoney(currency, xmlTrade.TradePrice)
	if err != nil {
		return nil, fmt.Errorf("parsing trade price: %w", err)
	}
	proceeds, err := moneypb.NewProtoMoney(currency, xmlTrade.Proceeds)
	if err != nil {
		return nil, fmt.Errorf("parsing proceeds: %w", err)
	}
	commission, err := moneypb.NewProtoMoney(currency, xmlTrade.IBCommission)
	if err != nil {
		return nil, fmt.Errorf("parsing commission: %w", err)
	}
	fifoPnlRealized, err := moneypb.NewProtoMoney(currency, xmlTrade.FifoPnlRealized)
	if err != nil {
		return nil, fmt.Errorf("parsing fifo pnl realized: %w", err)
	}
	// Parse the quantity as an integer.
	quantity, err := strconv.ParseInt(xmlTrade.Quantity, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing quantity %q: %w", xmlTrade.Quantity, err)
	}
	return &tradev1.Trade{
		TradeId:         xmlTrade.TradeID,
		TradeDate:       protoTradeDate,
		SettleDate:      protoSettleDate,
		Symbol:          xmlTrade.Symbol,
		Description:     xmlTrade.Description,
		AssetCategory:   xmlTrade.AssetCategory,
		BuySell:         xmlTrade.BuySell,
		Quantity:        quantity,
		TradePrice:      tradePrice,
		Proceeds:        proceeds,
		Commission:      commission,
		Currency:        currency,
		FifoPnlRealized: fifoPnlRealized,
	}, nil
}

// xmlPositionToProto converts an XML position from the Flex Query response to a proto Position.
func xmlPositionToProto(xmlPosition *flexquery.XMLPosition) (*positionv1.Position, error) {
	currency := xmlPosition.Currency
	// Parse numeric fields using the Money proto helper.
	costBasisPrice, err := moneypb.NewProtoMoney(currency, xmlPosition.CostBasisPrice)
	if err != nil {
		return nil, fmt.Errorf("parsing cost basis price: %w", err)
	}
	marketPrice, err := moneypb.NewProtoMoney(currency, xmlPosition.MarkPrice)
	if err != nil {
		return nil, fmt.Errorf("parsing market price: %w", err)
	}
	marketValue, err := moneypb.NewProtoMoney(currency, xmlPosition.PositionValue)
	if err != nil {
		return nil, fmt.Errorf("parsing market value: %w", err)
	}
	fifoPnlUnrealized, err := moneypb.NewProtoMoney(currency, xmlPosition.FifoPnlUnrealized)
	if err != nil {
		return nil, fmt.Errorf("parsing fifo pnl unrealized: %w", err)
	}
	// Parse the quantity as an integer.
	quantity, err := strconv.ParseInt(xmlPosition.Quantity, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing quantity %q: %w", xmlPosition.Quantity, err)
	}
	return &positionv1.Position{
		Symbol:            xmlPosition.Symbol,
		Description:       xmlPosition.Description,
		AssetCategory:     xmlPosition.AssetCategory,
		Quantity:          quantity,
		CostBasisPrice:    costBasisPrice,
		MarketPrice:       marketPrice,
		MarketValue:       marketValue,
		FifoPnlUnrealized: fifoPnlUnrealized,
		Currency:          currency,
	}, nil
}

// parseIBKRDate parses an IBKR date string in YYYYMMDD format.
func parseIBKRDate(s string) (time.Time, error) {
	return time.Parse("20060102", s)
}
