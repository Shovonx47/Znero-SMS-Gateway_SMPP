package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"

	"github.com/xuri/excelize/v2"
)

// Generator defines output generator methods.
type Generator struct{}

func NewGenerator() *Generator {
	return &Generator{}
}

// GenerateCSV generates sanitized CSV bytes.
func (g *Generator) GenerateCSV(headers []string, rows [][]string) ([]byte, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)

	if len(headers) > 0 {
		sanitizedHeaders := make([]string, len(headers))
		for i, h := range headers {
			sanitizedHeaders[i] = SanitizeCSVCell(h)
		}
		if err := writer.Write(sanitizedHeaders); err != nil {
			return nil, fmt.Errorf("failed to write csv headers: %w", err)
		}
	}

	for _, row := range rows {
		sanitizedRow := make([]string, len(row))
		for i, col := range row {
			sanitizedRow[i] = SanitizeCSVCell(col)
		}
		if err := writer.Write(sanitizedRow); err != nil {
			return nil, fmt.Errorf("failed to write csv row: %w", err)
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("csv writer flush error: %w", err)
	}

	return buf.Bytes(), nil
}

// GenerateJSON generates JSON formatted array bytes.
func (g *Generator) GenerateJSON(records []map[string]interface{}) ([]byte, error) {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal json export: %w", err)
	}
	return data, nil
}

// GenerateXLSX generates an Excel XLSX file bytes with formula sanitization.
func (g *Generator) GenerateXLSX(sheetName string, headers []string, rows [][]string) ([]byte, error) {
	if sheetName == "" {
		sheetName = "Export"
	}

	f := excelize.NewFile()
	defer func() {
		_ = f.Close()
	}()

	// Rename default sheet or create
	defaultSheet := f.GetSheetName(0)
	if defaultSheet != "" {
		_ = f.SetSheetName(defaultSheet, sheetName)
	} else {
		_, _ = f.NewSheet(sheetName)
	}

	// Write headers
	for colIdx, header := range headers {
		cell, err := excelize.CoordinatesToCellName(colIdx+1, 1)
		if err != nil {
			return nil, fmt.Errorf("coordinates error: %w", err)
		}
		if err := f.SetCellValue(sheetName, cell, SanitizeCSVCell(header)); err != nil {
			return nil, fmt.Errorf("set cell value error: %w", err)
		}
	}

	// Write rows
	for rIdx, row := range rows {
		rowNumber := rIdx + 2
		for colIdx, colVal := range row {
			cell, err := excelize.CoordinatesToCellName(colIdx+1, rowNumber)
			if err != nil {
				return nil, fmt.Errorf("coordinates error: %w", err)
			}
			if err := f.SetCellValue(sheetName, cell, SanitizeCSVCell(colVal)); err != nil {
				return nil, fmt.Errorf("set cell value error: %w", err)
			}
		}
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, fmt.Errorf("failed to write excel buffer: %w", err)
	}

	return buf.Bytes(), nil
}
