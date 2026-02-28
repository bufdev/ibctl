// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package moneypb provides helper functions for working with proto Money messages.
package moneypb

import (
	"math"

	mathv1 "github.com/bufdev/ibctl/internal/gen/proto/go/standard/math/v1"
	moneyv1 "github.com/bufdev/ibctl/internal/gen/proto/go/standard/money/v1"
	"github.com/bufdev/ibctl/internal/pkg/mathpb"
)

// NewProtoMoney creates a new Money proto from a currency code and a decimal string value.
func NewProtoMoney(currencyCode string, value string) (*moneyv1.Money, error) {
	decimal, err := mathpb.NewDecimal(value)
	if err != nil {
		return nil, err
	}
	return &moneyv1.Money{
		CurrencyCode: currencyCode,
		Amount:       decimal,
	}, nil
}

// MoneyToMicros converts a Money proto to its total micros representation.
func MoneyToMicros(money *moneyv1.Money) int64 {
	if money == nil {
		return 0
	}
	return mathpb.ToMicros(money.GetAmount())
}

// MoneyFromMicros creates a Money proto from total micros and a currency code.
func MoneyFromMicros(currencyCode string, totalMicros int64) *moneyv1.Money {
	return &moneyv1.Money{
		CurrencyCode: currencyCode,
		Amount:       mathpb.FromMicros(totalMicros),
	}
}

// MoneyValueToString converts a Money proto to a decimal string representation.
func MoneyValueToString(money *moneyv1.Money) string {
	if money == nil {
		return "0"
	}
	return mathpb.ToString(money.GetAmount())
}

// MoneyTimes multiplies a Money value by a given factor and returns a new Money.
func MoneyTimes(money *moneyv1.Money, factor int64) *moneyv1.Money {
	totalMicros := MoneyToMicros(money) * factor
	return MoneyFromMicros(money.GetCurrencyCode(), totalMicros)
}

// MoneyAdd adds two Money values with the same currency and returns a new Money.
func MoneyAdd(a, b *moneyv1.Money) *moneyv1.Money {
	totalMicros := MoneyToMicros(a) + MoneyToMicros(b)
	return MoneyFromMicros(a.GetCurrencyCode(), totalMicros)
}

// MoneyDivide divides a Money value by a divisor and returns a new Money.
// Rounds to nearest rather than truncating.
func MoneyDivide(money *moneyv1.Money, divisor int64) *moneyv1.Money {
	totalMicros := MoneyToMicros(money)
	result := int64(math.Round(float64(totalMicros) / float64(divisor)))
	return MoneyFromMicros(money.GetCurrencyCode(), result)
}

// ParseDecimalToUnitsMicros parses a decimal string into units and micros.
// Deprecated: Use mathpb.ParseToUnitsMicros instead.
func ParseDecimalToUnitsMicros(value string) (int64, int64, error) {
	return mathpb.ParseToUnitsMicros(value)
}

// NewDecimal creates a Decimal proto from a decimal string value.
// Convenience re-export for packages that import moneypb but also need Decimal.
func NewDecimal(value string) (*mathv1.Decimal, error) {
	return mathpb.NewDecimal(value)
}
