// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package cli provides CLI utility functions for file I/O, output formatting, and serialization.
package cli

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
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

// ForFile calls f for an opened *os.File opened for reading.
func ForFile(filePath string, f func(io.ReadWriter) error) (retErr error) {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer func() {
		retErr = errors.Join(retErr, file.Close())
	}()
	return f(file)
}

// ForWriteFile calls f for an opened *os.File opened for writing, creating the file if needed.
func ForWriteFile(filePath string, f func(io.Writer) error) (retErr error) {
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer func() {
		retErr = errors.Join(retErr, file.Close())
	}()
	return f(file)
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

// WriteProtoMessageJSON writes a single proto message as JSON to a file.
func WriteProtoMessageJSON(filePath string, message proto.Message) error {
	data, err := protojsonMarshal(message)
	if err != nil {
		return err
	}
	// Append a trailing newline for clean file formatting.
	data = append(data, '\n')
	return os.WriteFile(filePath, data, 0o644)
}

// ReadProtoMessageJSON reads a single proto message from a JSON file.
func ReadProtoMessageJSON(filePath string, message proto.Message) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	return protojsonUnmarshal(data, message)
}

// UnmarshalYAMLStrict unmarshals the data as YAML, returning a user error on failure.
//
// If the data length is 0, this is a no-op.
func UnmarshalYAMLStrict(data []byte, v any) error {
	if len(data) == 0 {
		return nil
	}
	yamlDecoder := NewYAMLDecoderStrict(bytes.NewReader(data))
	if err := yamlDecoder.Decode(v); err != nil {
		return fmt.Errorf("could not unmarshal as YAML: %w", err)
	}
	return nil
}

// NewYAMLDecoderStrict creates a new YAML decoder from the reader with strict field checking.
func NewYAMLDecoderStrict(reader io.Reader) *yaml.Decoder {
	yamlDecoder := yaml.NewDecoder(reader)
	// Reject unknown fields.
	yamlDecoder.KnownFields(true)
	return yamlDecoder
}

// ExpandHome expands a leading ~ in a path to the user's home directory.
func ExpandHome(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not get home directory: %w", err)
	}
	return filepath.Join(homeDir, path[1:]), nil
}

// protojsonMarshal marshals a proto message to JSON using proto field names.
func protojsonMarshal(message proto.Message) ([]byte, error) {
	return (protojson.MarshalOptions{UseProtoNames: true}).Marshal(message)
}

// protojsonUnmarshal unmarshals JSON data into a proto message.
func protojsonUnmarshal(data []byte, message proto.Message) error {
	return (protojson.UnmarshalOptions{}).Unmarshal(data, message)
}
