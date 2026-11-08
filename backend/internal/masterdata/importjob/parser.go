package importjob

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

const (
	MaxFileSize           = 50 * 1024 * 1024  // 50MB
	MaxRowCount           = 20000             // Maximum 20,000 data rows per import (for non-rate imports)
	MaxRatingRowCount     = 150000            // Maximum 150,000 data rows per import (supporting >=100,000 wholesale rate sheets)
	MaxColumnCount        = 50                // Maximum 50 columns per row
	MaxDecompressedSize   = 100 * 1024 * 1024 // 100MB max total uncompressed zip size
	MaxDecompressionRatio = 100               // 100:1 max decompression ratio
)

var (
	ErrFileTooLarge    = errors.New("uploaded file exceeds the maximum size limit of 50MB")
	ErrUnsupportedType = errors.New("unsupported file format: only CSV (.csv), Excel (.xlsx), and JSON (.json) are permitted")
	ErrEmptyFile       = errors.New("uploaded file is empty")
	ErrInvalidJSON     = errors.New("invalid JSON format: must be an array of objects")
	ErrMimeMismatch    = errors.New("content-type sniffing mismatch: file content does not match allowed format")
	ErrTooManyRows     = errors.New("file exceeds maximum allowed row count limit of 20,000 rows")
	ErrTooManyColumns  = errors.New("file exceeds maximum allowed column count limit of 50 columns")
	ErrZipBombDetected = errors.New("security check failed: zip bomb detected in compressed spreadsheet archive")
)

type ParsedRow struct {
	RowNumber int
	Data      map[string]string
}

// ValidateUpload performs basic file metadata validation, extension checks,
// and path-traversal sanitization.
func ValidateUpload(fileName string, fileSize int64) (string, error) {
	if fileSize > MaxFileSize {
		return "", ErrFileTooLarge
	}
	if fileSize == 0 {
		return "", ErrEmptyFile
	}

	// Security: Prevent path traversal in file_name
	cleanName := filepath.Base(fileName)
	ext := strings.ToLower(filepath.Ext(cleanName))

	switch ext {
	case ".csv", ".xlsx", ".json":
		return ext, nil
	default:
		return "", ErrUnsupportedType
	}
}

// ValidateContentSniffing inspects the initial byte headers (MIME sniffing)
// and inspects archive compression ratios to defeat disguised binaries and zip bombs.
func ValidateContentSniffing(ext string, data []byte) error {
	if len(data) == 0 {
		return ErrEmptyFile
	}

	sampleLen := 512
	if len(data) < sampleLen {
		sampleLen = len(data)
	}
	sniffed := http.DetectContentType(data[:sampleLen])

	switch ext {
	case ".csv":
		// Disallow binary executables, media, and pdf disguised as csv
		if strings.HasPrefix(sniffed, "application/x-") ||
			strings.HasPrefix(sniffed, "image/") ||
			strings.HasPrefix(sniffed, "video/") ||
			sniffed == "application/pdf" {
			return fmt.Errorf("%w: detected %s for .csv", ErrMimeMismatch, sniffed)
		}
		// Confirm data contains valid UTF-8 text
		if !utf8.Valid(data[:sampleLen]) {
			return fmt.Errorf("%w: binary non-UTF-8 bytes detected in .csv", ErrMimeMismatch)
		}

	case ".json":
		if strings.HasPrefix(sniffed, "application/x-") ||
			strings.HasPrefix(sniffed, "image/") ||
			strings.HasPrefix(sniffed, "video/") ||
			sniffed == "application/pdf" {
			return fmt.Errorf("%w: detected %s for .json", ErrMimeMismatch, sniffed)
		}
		trimmed := bytes.TrimSpace(data)
		if len(trimmed) > 0 && trimmed[0] != '[' {
			return fmt.Errorf("%w: json must begin with array bracket '['", ErrInvalidJSON)
		}

	case ".xlsx":
		// Excel (.xlsx) is an OpenXML PKZip archive; verify ZIP magic bytes: PK\x03\x04
		if len(data) < 4 || data[0] != 0x50 || data[1] != 0x4B || data[2] != 0x03 || data[3] != 0x04 {
			return fmt.Errorf("%w: missing standard ZIP PK header for .xlsx", ErrMimeMismatch)
		}
		// Zip-bomb decompression ratio and total uncompressed size validation
		if err := validateZipBomb(data); err != nil {
			return err
		}

	default:
		return ErrUnsupportedType
	}

	return nil
}

func validateZipBomb(data []byte) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("read zip archive: %w", err)
	}

	var totalUncompressed uint64
	compressedSize := uint64(len(data))
	if compressedSize == 0 {
		compressedSize = 1
	}

	for _, file := range zr.File {
		clean := filepath.Clean(file.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("malicious zip archive: path traversal in entry %q", file.Name)
		}

		totalUncompressed += file.UncompressedSize64
		if totalUncompressed > MaxDecompressedSize {
			return fmt.Errorf("%w: uncompressed size %d bytes exceeds %d bytes limit", ErrZipBombDetected, totalUncompressed, MaxDecompressedSize)
		}
	}

	ratio := totalUncompressed / compressedSize
	if ratio > MaxDecompressionRatio {
		return fmt.Errorf("%w: decompression ratio %d:1 exceeds %d:1 limit", ErrZipBombDetected, ratio, MaxDecompressionRatio)
	}

	return nil
}

func ParseFile(ext string, r io.Reader) ([]ParsedRow, error) {
	return ParseFileWithLimit(ext, r, MaxRowCount)
}

func ParseFileWithLimit(ext string, r io.Reader, maxRows int) ([]ParsedRow, error) {
	if maxRows <= 0 {
		maxRows = MaxRowCount
	}
	switch ext {
	case ".csv":
		return parseCSV(r, maxRows)
	case ".xlsx":
		return parseXLSX(r, maxRows)
	case ".json":
		return parseJSON(r, maxRows)
	default:
		return nil, ErrUnsupportedType
	}
}

func parseCSV(r io.Reader, maxRows int) ([]ParsedRow, error) {
	reader := csv.NewReader(r)
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1

	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read csv: %w", err)
	}

	// Find the header row (skip empty rows and rows starting with '#')
	headerIdx := -1
	for idx, r := range records {
		if len(r) == 0 || (len(r) == 1 && strings.TrimSpace(r[0]) == "") {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(r[0]), "#") {
			continue
		}
		headerIdx = idx
		break
	}

	if headerIdx == -1 || headerIdx >= len(records)-1 {
		return nil, errors.New("CSV must contain at least a header line and one data row")
	}

	if len(records[headerIdx]) > MaxColumnCount {
		return nil, fmt.Errorf("%w: CSV has %d columns (max %d)", ErrTooManyColumns, len(records[headerIdx]), MaxColumnCount)
	}

	header := make([]string, len(records[headerIdx]))
	for i, col := range records[headerIdx] {
		clean := strings.TrimPrefix(col, "\ufeff")
		clean = strings.TrimSpace(strings.ToLower(clean))
		header[i] = clean
	}

	var parsed []ParsedRow
	rowNum := 1
	for lineIdx := headerIdx + 1; lineIdx < len(records); lineIdx++ {
		record := records[lineIdx]
		if len(record) == 0 || (len(record) == 1 && strings.TrimSpace(record[0]) == "") {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(record[0]), "#") {
			continue
		}

		if len(parsed) >= maxRows {
			return nil, fmt.Errorf("%w: CSV exceeds %d rows", ErrTooManyRows, maxRows)
		}

		m := make(map[string]string, len(header))
		for colIdx, colName := range header {
			if colName == "" {
				continue
			}
			val := ""
			if colIdx < len(record) {
				val = strings.TrimSpace(record[colIdx])
			}
			m[colName] = val
		}
		parsed = append(parsed, ParsedRow{
			RowNumber: rowNum,
			Data:      m,
		})
		rowNum++
	}

	return parsed, nil
}

func parseXLSX(r io.Reader, maxRows int) ([]ParsedRow, error) {
	buf, err := io.ReadAll(io.LimitReader(r, MaxFileSize+1024))
	if err != nil {
		return nil, fmt.Errorf("read excel bytes: %w", err)
	}

	f, err := excelize.OpenReader(bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("open excel file: %w", err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, errors.New("excel file contains no sheets")
	}

	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, fmt.Errorf("read excel sheet: %w", err)
	}

	headerIdx := -1
	for idx, r := range rows {
		if len(r) == 0 || (len(r) == 1 && strings.TrimSpace(r[0]) == "") {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(r[0]), "#") {
			continue
		}
		headerIdx = idx
		break
	}

	if headerIdx == -1 || headerIdx >= len(rows)-1 {
		return nil, errors.New("excel sheet must contain a header and at least one data row")
	}

	if len(rows[headerIdx]) > MaxColumnCount {
		return nil, fmt.Errorf("%w: Excel sheet has %d columns (max %d)", ErrTooManyColumns, len(rows[headerIdx]), MaxColumnCount)
	}

	header := make([]string, len(rows[headerIdx]))
	for i, col := range rows[headerIdx] {
		clean := strings.TrimPrefix(col, "\ufeff")
		clean = strings.TrimSpace(strings.ToLower(clean))
		header[i] = clean
	}

	var parsed []ParsedRow
	rowNum := 1
	for lineIdx := headerIdx + 1; lineIdx < len(rows); lineIdx++ {
		record := rows[lineIdx]
		if len(record) == 0 {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(record[0]), "#") {
			continue
		}

		if len(parsed) >= maxRows {
			return nil, fmt.Errorf("%w: Excel exceeds %d rows", ErrTooManyRows, maxRows)
		}

		m := make(map[string]string, len(header))
		for colIdx, colName := range header {
			if colName == "" {
				continue
			}
			val := ""
			if colIdx < len(record) {
				val = strings.TrimSpace(record[colIdx])
			}
			m[colName] = val
		}
		parsed = append(parsed, ParsedRow{
			RowNumber: rowNum,
			Data:      m,
		})
		rowNum++
	}

	return parsed, nil
}

func parseJSON(r io.Reader, maxRows int) ([]ParsedRow, error) {
	var rawItems []map[string]any
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&rawItems); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}

	if len(rawItems) > maxRows {
		return nil, fmt.Errorf("%w: JSON array contains %d items (max %d)", ErrTooManyRows, len(rawItems), maxRows)
	}

	var parsed []ParsedRow
	for idx, item := range rawItems {
		if len(item) > MaxColumnCount {
			return nil, fmt.Errorf("%w: JSON row %d has %d fields (max %d)", ErrTooManyColumns, idx+1, len(item), MaxColumnCount)
		}

		m := make(map[string]string, len(item))
		for k, v := range item {
			cleanKey := strings.TrimSpace(strings.ToLower(k))
			switch val := v.(type) {
			case string:
				m[cleanKey] = strings.TrimSpace(val)
			case float64:
				m[cleanKey] = fmt.Sprintf("%.0f", val)
			case bool:
				m[cleanKey] = fmt.Sprintf("%v", val)
			default:
				bytesVal, _ := json.Marshal(v)
				m[cleanKey] = string(bytesVal)
			}
		}
		parsed = append(parsed, ParsedRow{
			RowNumber: idx + 1,
			Data:      m,
		})
	}

	return parsed, nil
}

// StreamParseCSV streams rows from a CSV reader in batches of chunkSize,
// invoking fn for each chunk to minimize memory consumption on large files (up to 150,000 rows).
func StreamParseCSV(r io.Reader, chunkSize int, fn func(chunk []ParsedRow) error) error {
	if chunkSize <= 0 {
		chunkSize = 500
	}
	reader := csv.NewReader(r)
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1

	// Read first row to locate header
	var header []string
	for {
		record, err := reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("CSV file is empty or missing headers")
			}
			return fmt.Errorf("read csv header: %w", err)
		}
		if len(record) == 0 || (len(record) == 1 && strings.TrimSpace(record[0]) == "") {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(record[0]), "#") {
			continue
		}
		if len(record) > MaxColumnCount {
			return fmt.Errorf("%w: CSV has %d columns (max %d)", ErrTooManyColumns, len(record), MaxColumnCount)
		}
		header = make([]string, len(record))
		for i, col := range record {
			clean := strings.TrimPrefix(col, "\ufeff")
			header[i] = strings.TrimSpace(strings.ToLower(clean))
		}
		break
	}

	chunk := make([]ParsedRow, 0, chunkSize)
	rowNum := 1

	for {
		record, err := reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("read csv record: %w", err)
		}
		if len(record) == 0 || (len(record) == 1 && strings.TrimSpace(record[0]) == "") {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(record[0]), "#") {
			continue
		}

		m := make(map[string]string, len(header))
		for colIdx, colName := range header {
			if colName == "" {
				continue
			}
			val := ""
			if colIdx < len(record) {
				val = strings.TrimSpace(record[colIdx])
			}
			m[colName] = val
		}

		chunk = append(chunk, ParsedRow{
			RowNumber: rowNum,
			Data:      m,
		})
		rowNum++

		if len(chunk) >= chunkSize {
			if err := fn(chunk); err != nil {
				return err
			}
			chunk = chunk[:0]
		}
	}

	if len(chunk) > 0 {
		if err := fn(chunk); err != nil {
			return err
		}
	}

	return nil
}
