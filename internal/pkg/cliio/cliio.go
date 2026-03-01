// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package cliio provides output formatting for CLI commands (table, CSV, JSON).
package cliio

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/bufdev/ibctl/internal/pkg/mathpb"
	"github.com/bufdev/ibctl/internal/pkg/moneypb"
)

// Format represents the output format for CLI commands.
type Format string

const (
	// FormatTable is the default table output format.
	FormatTable Format = "table"
	// FormatCSV is the CSV output format.
	FormatCSV Format = "csv"
	// FormatJSON is the JSON output format.
	FormatJSON Format = "json"
)

// ParseFormat parses a string into a Format, returning an error for unknown formats.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(s) {
	case "table":
		return FormatTable, nil
	case "csv":
		return FormatCSV, nil
	case "json":
		return FormatJSON, nil
	default:
		return "", fmt.Errorf("unknown format %q, must be one of: table, csv, json", s)
	}
}

// WriteTable writes tabular data to the writer using tabwriter for aligned columns.
func WriteTable(writer io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(writer, 0, 0, 2, ' ', 0)
	// Write header row.
	if _, err := fmt.Fprintln(tw, strings.Join(headers, "\t")); err != nil {
		return err
	}
	// Write data rows.
	for _, row := range rows {
		if _, err := fmt.Fprintln(tw, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// WriteTableWithTotals writes a table followed by a blank line and a totals row,
// all through the same tabwriter so columns align between data and totals.
func WriteTableWithTotals(writer io.Writer, headers []string, rows [][]string, totalsRow []string) error {
	tw := tabwriter.NewWriter(writer, 0, 0, 2, ' ', 0)
	// Write header row.
	if _, err := fmt.Fprintln(tw, strings.Join(headers, "\t")); err != nil {
		return err
	}
	// Write data rows.
	for _, row := range rows {
		if _, err := fmt.Fprintln(tw, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	// Write a blank separator line with tabs to preserve column alignment.
	blankRow := make([]string, len(headers))
	if _, err := fmt.Fprintln(tw, strings.Join(blankRow, "\t")); err != nil {
		return err
	}
	// Write the totals row aligned to the same columns.
	if _, err := fmt.Fprintln(tw, strings.Join(totalsRow, "\t")); err != nil {
		return err
	}
	return tw.Flush()
}

// WriteCSVRecords writes CSV records to the writer.
func WriteCSVRecords(writer io.Writer, records [][]string) error {
	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.WriteAll(records); err != nil {
		return err
	}
	csvWriter.Flush()
	return nil
}

// FormatUSD formats a raw decimal string as a USD value with $ prefix,
// rounded to cents with comma separators (e.g., "$1,234.56", "-$789.01").
// Returns empty string for empty input.
func FormatUSD(value string) string {
	if value == "" {
		return ""
	}
	decimal, err := mathpb.NewDecimal(value)
	if err != nil {
		return value
	}
	formatted := mathpb.Format(decimal, 2)
	// Prepend $ after any negative sign.
	if len(formatted) > 0 && formatted[0] == '-' {
		return "-$" + formatted[1:]
	}
	return "$" + formatted
}

// FormatUSDMicros formats a micros value as a USD string with $ prefix.
func FormatUSDMicros(micros int64) string {
	return FormatUSD(moneypb.MoneyValueToString(moneypb.MoneyFromMicros("USD", micros)))
}

// WriteJSON writes objects as JSON with newlines between each object.
func WriteJSON[O any](writer io.Writer, objects ...O) error {
	for _, object := range objects {
		data, err := json.Marshal(object)
		if err != nil {
			return err
		}
		if _, err := writer.Write(data); err != nil {
			return err
		}
		if _, err := writer.Write([]byte("\n")); err != nil {
			return err
		}
	}
	return nil
}
