// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package mathpb provides helper functions for working with proto math messages (e.g., Decimal).
package mathpb

import (
	"fmt"
	"strconv"
	"strings"

	mathv1 "github.com/bufdev/ibctl/internal/gen/proto/go/standard/math/v1"
)

// microsFactor is the number of micros per unit.
const microsFactor = 1_000_000

// NewDecimal creates a Decimal proto from a decimal string value (e.g., "123.456789").
func NewDecimal(value string) (*mathv1.Decimal, error) {
	units, micros, err := ParseToUnitsMicros(value)
	if err != nil {
		return nil, fmt.Errorf("invalid decimal value %q: %w", value, err)
	}
	return &mathv1.Decimal{
		Units:  units,
		Micros: micros,
	}, nil
}

// ToMicros converts a Decimal proto to its total micros representation.
func ToMicros(d *mathv1.Decimal) int64 {
	if d == nil {
		return 0
	}
	return d.GetUnits()*microsFactor + d.GetMicros()
}

// FromMicros creates a Decimal proto from total micros.
func FromMicros(totalMicros int64) *mathv1.Decimal {
	return &mathv1.Decimal{
		Units:  totalMicros / microsFactor,
		Micros: totalMicros % microsFactor,
	}
}

// ToString converts a Decimal proto to a decimal string representation.
func ToString(d *mathv1.Decimal) string {
	if d == nil {
		return "0"
	}
	totalMicros := ToMicros(d)
	// Handle sign separately for correct formatting.
	sign := ""
	if totalMicros < 0 {
		sign = "-"
		totalMicros = -totalMicros
	}
	units := totalMicros / microsFactor
	micros := totalMicros % microsFactor
	if micros == 0 {
		return sign + strconv.FormatInt(units, 10)
	}
	// Format with up to 6 decimal places, trim trailing zeros.
	decimalStr := fmt.Sprintf("%06d", micros)
	decimalStr = strings.TrimRight(decimalStr, "0")
	return fmt.Sprintf("%s%d.%s", sign, units, decimalStr)
}

// Format formats a decimal value rounded to the given precision (number of
// decimal places) with comma separators for the integer portion.
// Examples with precision=2: 1234567.891 → "1,234,567.89", -42.5 → "-42.50".
// Examples with precision=0: 1234.5 → "1,235".
func Format(d *mathv1.Decimal, precision int) string {
	if d == nil {
		if precision > 0 {
			return "0." + strings.Repeat("0", precision)
		}
		return "0"
	}
	totalMicros := ToMicros(d)
	negative := totalMicros < 0
	if negative {
		totalMicros = -totalMicros
	}
	// Compute the divisor for the requested precision.
	// Micros has 6 decimal places; we need to reduce to the requested precision.
	// divisor = 10^(6-precision), rounding half = divisor/2.
	divisor := int64(1)
	for range 6 - precision {
		divisor *= 10
	}
	// Round to nearest unit at the requested precision.
	totalMicros = (totalMicros + divisor/2) / divisor
	// Split into integer and fractional parts at the requested precision.
	fracDivisor := int64(1)
	for range precision {
		fracDivisor *= 10
	}
	intPart := totalMicros / fracDivisor
	fracPart := totalMicros % fracDivisor
	sign := ""
	if negative {
		sign = "-"
	}
	if precision == 0 {
		return sign + addCommas(intPart)
	}
	return fmt.Sprintf("%s%s.%0*d", sign, addCommas(intPart), precision, fracPart)
}

// addCommas inserts comma separators into a non-negative integer (e.g., 1234567 → "1,234,567").
func addCommas(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	// Determine how many digits are before the first comma.
	firstGroup := len(s) % 3
	if firstGroup == 0 {
		firstGroup = 3
	}
	b.WriteString(s[:firstGroup])
	for i := firstGroup; i < len(s); i += 3 {
		b.WriteByte(',')
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// ParseToUnitsMicros parses a decimal string (e.g., "123.456789") into units and micros.
func ParseToUnitsMicros(value string) (int64, int64, error) {
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
