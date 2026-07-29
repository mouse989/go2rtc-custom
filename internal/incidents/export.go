package incidents

import (
	"io"
	"sort"

	"github.com/xuri/excelize/v2"
)

// WriteExcel writes list as an .xlsx matching the legacy column layout
// (STT, Thời gian, Thể loại, Nội dung, Đơn vị, Ghi chú, Vị trí, Khung giờ),
// oldest first — same order/shape staff are used to reading in the original
// spreadsheet, so an export can be handed off or re-imported unchanged.
func WriteExcel(w io.Writer, list []*Incident) error {
	sorted := make([]*Incident, len(list))
	copy(sorted, list)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Time.Before(sorted[j].Time) })

	f := excelize.NewFile()
	defer f.Close()

	const sheet = "Sự cố"
	f.SetSheetName("Sheet1", sheet)

	headers := []string{"STT", "Thời gian", "Thể loại", "Nội dung sự việc, tình trạng xử lý", "Đơn vị", "Ghi Chú", "Vị trí", "Khung giờ"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(sheet, cell, h)
	}
	boldStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	lastHeaderCell, _ := excelize.CoordinatesToCellName(len(headers), 1)
	_ = f.SetCellStyle(sheet, "A1", lastHeaderCell, boldStyle)

	dateStyle, _ := f.NewStyle(&excelize.Style{CustomNumFmt: strPtr("yyyy-mm-dd hh:mm")})

	for i, in := range sorted {
		row := i + 2
		set := func(col int, v any) {
			cell, _ := excelize.CoordinatesToCellName(col, row)
			_ = f.SetCellValue(sheet, cell, v)
		}
		set(1, i+1)
		// Excel has no timezone concept — it stores/displays whatever
		// wall-clock time.Time carries. Convert explicitly to Vietnam time
		// here rather than relying on in.Time already being VN-zoned, so
		// export is correct even for older records saved before storage was
		// unified (see validate() and RecomputeAllTimeSlots).
		set(2, in.Time.In(vnLocation))
		set(3, in.Category)
		set(4, in.Content)
		set(5, in.Unit)
		set(6, in.Note)
		set(7, in.Location)
		set(8, in.TimeSlot)

		timeCell, _ := excelize.CoordinatesToCellName(2, row)
		_ = f.SetCellStyle(sheet, timeCell, timeCell, dateStyle)
	}

	for i, width := range []float64{6, 16, 12, 60, 22, 20, 20, 16} {
		col, _ := excelize.ColumnNumberToName(i + 1)
		_ = f.SetColWidth(sheet, col, col, width)
	}
	_ = f.SetPanes(sheet, &excelize.Panes{Freeze: true, Split: false, XSplit: 0, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})

	if idx, err := f.GetSheetIndex(sheet); err == nil {
		f.SetActiveSheet(idx)
	}
	return f.Write(w)
}

func strPtr(s string) *string { return &s }
