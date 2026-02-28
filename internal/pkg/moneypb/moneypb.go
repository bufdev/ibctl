// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package moneypb provides helper functions for working with proto Money messages.
package moneypb

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	moneyv1 "github.com/bufdev/ibctl/internal/gen/proto/go/standard/money/v1"
)

// microsFactor is the number of micros per unit.
const microsFactor = 1_000_000

// NewProtoMoney creates a new Money proto from a currency code and a decimal string value.
func NewProtoMoney(currencyCode string, value string) (*moneyv1.Money, error) {
	units, micros, err := parseDecimalToUnitsMicros(value)
	if err != nil {
		return nil, fmt.Errorf("invalid money value %q: %w", value, err)
	}
	return &moneyv1.Money{
		CurrencyCode: currencyCode,
		Units:        units,
		Micros:       micros,
	}, nil
}

// MoneyTimes multiplies a Money value by a given factor and returns a new Money.
func MoneyTimes(money *moneyv1.Money, factor int64) *moneyv1.Money {
	// Convert to total micros, multiply, convert back.
	totalMicros := MoneyToMicros(money) * factor
	units := totalMicros / microsFactor
	micros := totalMicros % microsFactor
	return &moneyv1.Money{
		CurrencyCode: money.GetCurrencyCode(),
		Units:        units,
		Micros:       micros,
	}
}

// MoneyValueToString converts a Money proto to a decimal string representation.
func MoneyValueToString(money *moneyv1.Money) string {
	totalMicros := MoneyToMicros(money)
	// Handle sign separately for correct formatting.
	sign := ""
	if totalMicros < 0 {
		sign = "-"
		totalMicros = -totalMicros
	}
	units := totalMicros / microsFactor
	micros := totalMicros % microsFactor
	if micros == 0 {
		return fmt.Sprintf("%s%d", sign, units)
	}
	// Format with up to 6 decimal places, trim trailing zeros.
	decimalStr := fmt.Sprintf("%06d", micros)
	decimalStr = strings.TrimRight(decimalStr, "0")
	return fmt.Sprintf("%s%d.%s", sign, units, decimalStr)
}

// MoneyToMicros converts a Money proto to its total micros representation.
func MoneyToMicros(money *moneyv1.Money) int64 {
	return money.GetUnits()*microsFactor + money.GetMicros()
}

// MoneyFromMicros creates a Money proto from total micros and a currency code.
func MoneyFromMicros(currencyCode string, totalMicros int64) *moneyv1.Money {
	units := totalMicros / microsFactor
	micros := totalMicros % microsFactor
	return &moneyv1.Money{
		CurrencyCode: currencyCode,
		Units:        units,
		Micros:       micros,
	}
}

// parseDecimalToUnitsMicros parses a decimal string (e.g. "123.456789") into units and micros.
func parseDecimalToUnitsMicros(value string) (int64, int64, error) {
	if value == "" {
		return 0, 0, nil
	}
	// Handle sign.
	negative := false
	cleanValue := value
	if strings.HasPrefix(cleanValue, "-") {
		negative = true
		cleanValue = cleanValue[1:]
	}
	// Split on decimal point.
	parts := strings.SplitN(cleanValue, ".", 2)
	// Parse integer part.
	units, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil && parts[0] != "" {
		return 0, 0, fmt.Errorf("parsing units: %w", err)
	}
	var micros int64
	if len(parts) == 2 && parts[1] != "" {
		// Pad or truncate to 6 decimal places.
		decimalStr := parts[1]
		if len(decimalStr) > 6 {
			decimalStr = decimalStr[:6]
		}
		for len(decimalStr) < 6 {
			decimalStr += "0"
		}
		micros, err = strconv.ParseInt(decimalStr, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("parsing micros: %w", err)
		}
	}
	if negative {
		// Negate non-zero values, leaving zero as-is to avoid -0.
		if units != 0 {
			units = -units
		}
		if micros != 0 {
			micros = -micros
		}
	}
	// Validate micros range.
	if micros < -999999 || micros > 999999 {
		return 0, 0, fmt.Errorf("micros out of range: %d", micros)
	}
	// Validate sign consistency.
	if units > 0 && micros < 0 {
		return 0, 0, fmt.Errorf("sign mismatch: units=%d micros=%d", units, micros)
	}
	if units < 0 && micros > 0 {
		return 0, 0, fmt.Errorf("sign mismatch: units=%d micros=%d", units, micros)
	}
	return units, micros, nil
}

// MoneyAdd adds two Money values with the same currency and returns a new Money.
func MoneyAdd(a, b *moneyv1.Money) *moneyv1.Money {
	totalMicros := MoneyToMicros(a) + MoneyToMicros(b)
	return MoneyFromMicros(a.GetCurrencyCode(), totalMicros)
}

// MoneyDivide divides a Money value by a divisor and returns a new Money.
// Uses integer division with rounding toward zero.
func MoneyDivide(money *moneyv1.Money, divisor int64) *moneyv1.Money {
	totalMicros := MoneyToMicros(money)
	// Round to nearest rather than truncate.
	result := int64(math.Round(float64(totalMicros) / float64(divisor)))
	return MoneyFromMicros(money.GetCurrencyCode(), result)
}
