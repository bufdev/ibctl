// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package ibkractivitycsv parses IBKR Activity Statement CSV files.
//
// Activity Statement CSVs are multi-section files where each row starts with
// a section name and row type (Header, Data, SubTotal, Total). Different sections
// have different column layouts. This parser extracts trades, positions, dividends,
// interest, withholding tax, and financial instrument information.
//
// Account Information sections are intentionally skipped to avoid reading
// identifying information like account numbers.
package ibkractivitycsv

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ActivityStatement contains all parsed sections from a single Activity Statement CSV file.
type ActivityStatement struct {
	// Trades contains stock/equity trade executions.
	Trades []Trade
	// ForexTrades contains foreign exchange conversion trades.
	ForexTrades []ForexTrade
	// Positions contains open position summaries.
	Positions []Position
	// Dividends contains dividend payments received.
	Dividends []Dividend
	// WithholdingTaxes contains taxes withheld on dividends.
	WithholdingTaxes []WithholdingTax
	// InterestItems contains interest income and expenses.
	InterestItems []Interest
	// InstrumentInfos contains financial instrument metadata.
	InstrumentInfos []InstrumentInfo
}

// Trade represents a stock/equity trade execution.
type Trade struct {
	Symbol       string
	DateTime     time.Time
	CurrencyCode string
	// Quantity is positive for buys, negative for sells.
	Quantity   string
	TradePrice string
	Proceeds   string
	Commission string
	RealizedPL string
	Code       string
}

// ForexTrade represents a foreign exchange conversion trade.
type ForexTrade struct {
	Symbol       string
	DateTime     time.Time
	CurrencyCode string
	Quantity     string
	TradePrice   string
	Proceeds     string
	Commission   string
}

// Position represents an open position summary.
type Position struct {
	Symbol        string
	AssetCategory string
	CurrencyCode  string
	Quantity      string
	CostPrice     string
	CostBasis     string
	ClosePrice    string
	Value         string
	UnrealizedPL  string
}

// Dividend represents a dividend payment.
type Dividend struct {
	CurrencyCode string
	Date         time.Time
	Description  string
	Amount       string
}

// WithholdingTax represents a tax withheld on a dividend.
type WithholdingTax struct {
	CurrencyCode string
	Date         time.Time
	Description  string
	Amount       string
	Code         string
}

// Interest represents an interest income or expense.
type Interest struct {
	CurrencyCode string
	Date         time.Time
	Description  string
	Amount       string
}

// InstrumentInfo contains financial instrument metadata.
type InstrumentInfo struct {
	AssetCategory   string
	Symbol          string
	Description     string
	Conid           string
	SecurityID      string
	ListingExchange string
	InstrumentType  string
	// Issuer is only set for bonds.
	Issuer string
	// Maturity is only set for bonds.
	Maturity string
}

// ParseDirectory reads all *.csv files recursively from the directory and parses them.
func ParseDirectory(dirPath string) ([]*ActivityStatement, error) {
	var statements []*ActivityStatement
	err := filepath.WalkDir(dirPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".csv") {
			return nil
		}
		statement, err := ParseFile(path)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		statements = append(statements, statement)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return statements, nil
}

// ParseFile parses a single IBKR Activity Statement CSV file.
func ParseFile(filePath string) (*ActivityStatement, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return parse(file)
}

func parse(reader io.Reader) (*ActivityStatement, error) {
	csvReader := csv.NewReader(reader)
	// Allow variable number of fields per record (sections have different column counts).
	csvReader.FieldsPerRecord = -1
	// Don't treat leading spaces as significant.
	csvReader.TrimLeadingSpace = true

	statement := &ActivityStatement{}
	// Track the current header for each section to map column indices.
	sectionHeaders := make(map[string][]string)

	for {
		record, err := csvReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading CSV: %w", err)
		}
		if len(record) < 2 {
			continue
		}
		sectionName := record[0]
		rowType := record[1]

		// Skip Account Information entirely — contains identifying info.
		if sectionName == "Account Information" {
			continue
		}

		// Track headers for each section.
		if rowType == "Header" {
			sectionHeaders[sectionName] = record
			continue
		}

		// Only process Data rows (skip SubTotal, Total, Notes).
		if rowType != "Data" {
			continue
		}

		switch sectionName {
		case "Trades":
			if err := parseTrade(record, sectionHeaders[sectionName], statement); err != nil {
				return nil, fmt.Errorf("parsing trade: %w", err)
			}
		case "Open Positions":
			if err := parsePosition(record, sectionHeaders[sectionName], statement); err != nil {
				return nil, fmt.Errorf("parsing position: %w", err)
			}
		case "Dividends":
			if err := parseDividend(record, statement); err != nil {
				return nil, fmt.Errorf("parsing dividend: %w", err)
			}
		case "Withholding Tax":
			if err := parseWithholdingTax(record, statement); err != nil {
				return nil, fmt.Errorf("parsing withholding tax: %w", err)
			}
		case "Interest":
			if err := parseInterest(record, statement); err != nil {
				return nil, fmt.Errorf("parsing interest: %w", err)
			}
		case "Financial Instrument Information":
			if err := parseInstrumentInfo(record, sectionHeaders[sectionName], statement); err != nil {
				return nil, fmt.Errorf("parsing instrument info: %w", err)
			}
		}
	}
	return statement, nil
}

// parseTrade parses a Trades,Data row. Only processes Order rows for Stocks and Forex.
func parseTrade(record []string, header []string, statement *ActivityStatement) error {
	if len(record) < 3 {
		return nil
	}
	// DataDiscriminator is field index 2. Only process "Order" rows.
	dataDiscriminator := record[2]
	if dataDiscriminator != "Order" {
		return nil
	}
	if len(record) < 8 {
		return nil
	}
	assetCategory := record[3]
	currencyCode := record[4]
	symbol := record[5]
	dateTimeStr := record[6]

	dateTime, err := parseDateTime(dateTimeStr)
	if err != nil {
		return fmt.Errorf("parsing date %q for %s: %w", dateTimeStr, symbol, err)
	}

	switch assetCategory {
	case "Stocks":
		// Stock trades have: DataDiscriminator,Asset Category,Currency,Symbol,Date/Time,Quantity,T. Price,C. Price,Proceeds,Comm/Fee,Basis,Realized P/L,MTM P/L,Code
		if len(record) < 16 {
			return nil
		}
		statement.Trades = append(statement.Trades, Trade{
			Symbol:       symbol,
			DateTime:     dateTime,
			CurrencyCode: currencyCode,
			Quantity:     cleanNumber(record[7]),
			TradePrice:   cleanNumber(record[8]),
			Proceeds:     cleanNumber(record[10]),
			Commission:   cleanNumber(record[11]),
			RealizedPL:   cleanNumber(record[13]),
			Code:         record[15],
		})
	case "Forex":
		// Forex trades have a different header: DataDiscriminator,Asset Category,Currency,Symbol,Date/Time,Quantity,T. Price,,Proceeds,Comm in USD,,,MTM in USD,Code
		if len(record) < 11 {
			return nil
		}
		statement.ForexTrades = append(statement.ForexTrades, ForexTrade{
			Symbol:       symbol,
			DateTime:     dateTime,
			CurrencyCode: currencyCode,
			Quantity:     cleanNumber(record[7]),
			TradePrice:   cleanNumber(record[8]),
			Proceeds:     cleanNumber(record[10]),
			Commission:   cleanNumber(record[11]),
		})
	}
	return nil
}

// parsePosition parses an Open Positions,Data row. Only processes Summary rows.
func parsePosition(record []string, header []string, statement *ActivityStatement) error {
	if len(record) < 3 {
		return nil
	}
	// DataDiscriminator is field index 2. Only process "Summary" rows.
	if record[2] != "Summary" {
		return nil
	}
	if len(record) < 13 {
		return nil
	}
	statement.Positions = append(statement.Positions, Position{
		Symbol:        record[5],
		AssetCategory: record[3],
		CurrencyCode:  record[4],
		Quantity:      cleanNumber(record[6]),
		CostPrice:     cleanNumber(record[8]),
		CostBasis:     cleanNumber(record[9]),
		ClosePrice:    cleanNumber(record[10]),
		Value:         cleanNumber(record[11]),
		UnrealizedPL:  cleanNumber(record[12]),
	})
	return nil
}

// parseDividend parses a Dividends,Data row. Skips Total rows.
func parseDividend(record []string, statement *ActivityStatement) error {
	if len(record) < 5 {
		return nil
	}
	currencyCode := record[2]
	// Skip Total/summary rows.
	if strings.HasPrefix(currencyCode, "Total") {
		return nil
	}
	date, err := parseDate(record[3])
	if err != nil {
		return fmt.Errorf("parsing dividend date %q: %w", record[3], err)
	}
	statement.Dividends = append(statement.Dividends, Dividend{
		CurrencyCode: currencyCode,
		Date:         date,
		Description:  record[4],
		Amount:       cleanNumber(record[5]),
	})
	return nil
}

// parseWithholdingTax parses a Withholding Tax,Data row. Skips Total rows.
func parseWithholdingTax(record []string, statement *ActivityStatement) error {
	if len(record) < 6 {
		return nil
	}
	currencyCode := record[2]
	// Skip Total/summary rows.
	if strings.HasPrefix(currencyCode, "Total") {
		return nil
	}
	date, err := parseDate(record[3])
	if err != nil {
		return fmt.Errorf("parsing withholding tax date %q: %w", record[3], err)
	}
	code := ""
	if len(record) > 6 {
		code = record[6]
	}
	statement.WithholdingTaxes = append(statement.WithholdingTaxes, WithholdingTax{
		CurrencyCode: currencyCode,
		Date:         date,
		Description:  record[4],
		Amount:       cleanNumber(record[5]),
		Code:         code,
	})
	return nil
}

// parseInterest parses an Interest,Data row. Skips Total rows.
func parseInterest(record []string, statement *ActivityStatement) error {
	if len(record) < 5 {
		return nil
	}
	currencyCode := record[2]
	// Skip Total/summary rows.
	if strings.HasPrefix(currencyCode, "Total") {
		return nil
	}
	date, err := parseDate(record[3])
	if err != nil {
		return fmt.Errorf("parsing interest date %q: %w", record[3], err)
	}
	statement.InterestItems = append(statement.InterestItems, Interest{
		CurrencyCode: currencyCode,
		Date:         date,
		Description:  record[4],
		Amount:       cleanNumber(record[5]),
	})
	return nil
}

// parseInstrumentInfo parses a Financial Instrument Information,Data row.
// Handles two header variants: Stocks (without Issuer/Maturity) and Bonds (with Issuer/Maturity).
func parseInstrumentInfo(record []string, header []string, statement *ActivityStatement) error {
	if len(record) < 11 {
		return nil
	}
	info := InstrumentInfo{
		AssetCategory:   record[2],
		Symbol:          record[3],
		Description:     record[4],
		Conid:           record[5],
		SecurityID:      record[6],
		ListingExchange: record[8],
		InstrumentType:  record[10],
	}
	// Bonds have additional Issuer and Maturity fields (header has 14 columns).
	if len(record) >= 13 {
		// Check if the header includes Issuer (bonds variant).
		if len(header) >= 13 && header[11] == "Issuer" {
			info.Issuer = record[11]
			info.Maturity = record[12]
		}
	}
	statement.InstrumentInfos = append(statement.InstrumentInfos, info)
	return nil
}

// parseDateTime parses an IBKR date/time string in "2026-01-02, 09:30:00" format.
func parseDateTime(s string) (time.Time, error) {
	return time.Parse("2006-01-02, 15:04:05", s)
}

// parseDate parses an IBKR date string in "2026-01-02" format.
func parseDate(s string) (time.Time, error) {
	return time.Parse("2006-01-02", s)
}

// cleanNumber strips commas from numeric strings (e.g., "-2,290" → "-2290").
func cleanNumber(s string) string {
	return strings.ReplaceAll(s, ",", "")
}
