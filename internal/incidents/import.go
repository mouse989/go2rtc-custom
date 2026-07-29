package incidents

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// ImportResult summarizes one Excel import run.
type ImportResult struct {
	TotalRows int      `json:"total_rows"`
	Imported  int      `json:"imported"`
	Skipped   int      `json:"skipped"`
	DryRun    bool     `json:"dry_run"`
	Errors    []string `json:"errors,omitempty"` // capped sample of row-level errors
}

const maxImportErrors = 50

// ImportExcel reads an .xlsx matching the legacy "Thời gian / Thể loại / Nội
// dung / Đơn vị / Ghi chú / Vị trí" column layout (columns matched by header
// text, not fixed position, so minor header reordering still works) and
// creates one Incident per data row. Every sheet in the workbook is scanned
// — a multi-year export with one sheet per year works in a single upload
// since each row's own date routes it to the right year file.
func ImportExcel(r io.Reader, username string, dryRun bool) (*ImportResult, error) {
	f, err := excelize.OpenReader(r)
	if err != nil {
		return nil, fmt.Errorf("không đọc được file Excel: %w", err)
	}
	defer f.Close()

	res := &ImportResult{DryRun: dryRun}

	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil || len(rows) < 2 {
			continue
		}
		col := mapHeaders(rows[0])
		if col.timeCol < 0 || col.contentCol < 0 {
			continue // doesn't look like an incidents sheet — skip silently
		}

		for i := 1; i < len(rows); i++ {
			row := rows[i]
			if isBlankRow(row) {
				continue
			}
			res.TotalRows++

			in, err := rowToIncident(row, col)
			if err != nil {
				res.Skipped++
				if len(res.Errors) < maxImportErrors {
					res.Errors = append(res.Errors, fmt.Sprintf("%s!%d: %s", sheet, i+1, err))
				}
				continue
			}

			if !dryRun {
				if err := Create(in, username); err != nil {
					res.Skipped++
					if len(res.Errors) < maxImportErrors {
						res.Errors = append(res.Errors, fmt.Sprintf("%s!%d: %s", sheet, i+1, err))
					}
					continue
				}
			}
			res.Imported++
		}
	}

	return res, nil
}

type headerCols struct {
	timeCol, categoryCol, contentCol, unitCol, noteCol, locationCol int
}

// mapHeaders matches columns by header text (contains, case-insensitive) so
// the importer tolerates minor header rewording/reordering across years.
func mapHeaders(header []string) headerCols {
	c := headerCols{-1, -1, -1, -1, -1, -1}
	for i, h := range header {
		h := strings.ToLower(strings.TrimSpace(h))
		switch {
		case strings.Contains(h, "thời gian"):
			c.timeCol = i
		case strings.Contains(h, "thể loại"):
			c.categoryCol = i
		case strings.Contains(h, "nội dung"):
			c.contentCol = i
		case strings.Contains(h, "đơn vị"):
			c.unitCol = i
		case strings.Contains(h, "ghi ch"): // "ghi chú"
			c.noteCol = i
		case strings.Contains(h, "vị trí"):
			c.locationCol = i
		}
	}
	return c
}

func cellAt(row []string, idx int) string {
	if idx < 0 || idx >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idx])
}

func isBlankRow(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// rowToIncident extracts one row's fields and applies the same required-field
// rule as manual entry (validate, shared with Create/Update): Thời gian, Thể
// loại, Nội dung, Đơn vị. Vị trí/Ghi chú are optional — a meaningful share of
// the legacy data has no location (general/area-wide reports).
func rowToIncident(row []string, col headerCols) (*Incident, error) {
	t, err := parseExcelTime(cellAt(row, col.timeCol))
	if err != nil {
		return nil, fmt.Errorf("thời gian không hợp lệ: %w", err)
	}
	in := &Incident{
		Time:     t,
		Category: cellAt(row, col.categoryCol),
		Content:  cellAt(row, col.contentCol),
		Unit:     cellAt(row, col.unitCol),
		Note:     cellAt(row, col.noteCol),
		Location: cellAt(row, col.locationCol),
	}
	if err := validate(in); err != nil {
		return nil, err
	}
	return in, nil
}

// timeLayouts is tried in order; the Vietnamese-convention DD/MM/YYYY forms
// come before the ambiguous-looking MM/DD/YYYY ones on purpose.
var timeLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"02/01/2006 15:04:05",
	"02/01/2006 15:04",
	"2/1/2006 15:04:05",
	"2/1/2006 15:04",
	"2006-01-02T15:04:05",
	"01/02/2006 15:04:05",
	"01/02/2006 15:04",
}

// parseExcelTime always interprets the cell as Vietnam wall-clock time
// (vnLocation), never time.Local — Excel/Google Sheets timestamps carry no
// timezone info of their own, and time.Local depends on the server process's
// OS timezone, which may not be Asia/Ho_Chi_Minh even though the data always
// is (CSGT staff enter times in HCMC local time regardless of where go2rtc
// happens to be hosted).
func parseExcelTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("để trống")
	}
	for _, layout := range timeLayouts {
		if t, err := time.ParseInLocation(layout, s, vnLocation); err == nil {
			return t, nil
		}
	}
	// Fallback: raw Excel serial date number (e.g. a cell excelize couldn't
	// auto-format as text, returned as its underlying float). ExcelDateToTime
	// returns the naive wall-clock value tagged UTC; re-tag as vnLocation
	// (same wall-clock digits, correct zone) rather than treating it as UTC.
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		if t, err := excelize.ExcelDateToTime(f, false); err == nil {
			wall := t.UTC()
			return time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), wall.Nanosecond(), vnLocation), nil
		}
	}
	return time.Time{}, fmt.Errorf("không nhận dạng được định dạng %q", s)
}
