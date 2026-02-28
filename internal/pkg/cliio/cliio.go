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

// WriteCSVRecords writes CSV records to the writer.
func WriteCSVRecords(writer io.Writer, records [][]string) error {
	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.WriteAll(records); err != nil {
		return err
	}
	csvWriter.Flush()
	return nil
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
