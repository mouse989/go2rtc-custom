package incidents

import (
	"bytes"
	"testing"
	"time"
)

func baseIncident() *Incident {
	return &Incident{
		Time:     time.Now().In(vnLocation),
		Category: "Va chạm",
		Content:  "Va chạm giữa 2 xe máy",
		Unit:     "ĐỘI CSGT BẾN THÀNH",
	}
}

func TestValidateSeverityRange(t *testing.T) {
	// 0 (unset) is always fine — Severity is optional.
	in := baseIncident()
	in.Severity = 0
	if err := validate(in); err != nil {
		t.Fatalf("severity 0 (unset) should be valid, got: %v", err)
	}

	for _, v := range []int{1, 2, 3, 4} {
		in := baseIncident()
		in.Severity = v
		if err := validate(in); err != nil {
			t.Fatalf("severity %d should be valid, got: %v", v, err)
		}
	}

	for _, v := range []int{-1, 5, 100} {
		in := baseIncident()
		in.Severity = v
		if err := validate(in); err == nil {
			t.Fatalf("severity %d should be rejected, got nil error", v)
		}
	}
}

func TestValidateLatLngPairing(t *testing.T) {
	// Neither set — fine (the overwhelming common case).
	in := baseIncident()
	if err := validate(in); err != nil {
		t.Fatalf("no coordinates should be valid, got: %v", err)
	}

	// Both set — fine.
	in = baseIncident()
	in.Lat, in.Lng = 10.776, 106.700
	if err := validate(in); err != nil {
		t.Fatalf("both lat+lng set should be valid, got: %v", err)
	}

	// Only one set — rejected (a half coordinate is a bug, not valid data).
	in = baseIncident()
	in.Lat = 10.776
	if err := validate(in); err == nil {
		t.Fatal("lat without lng should be rejected, got nil error")
	}

	in = baseIncident()
	in.Lng = 106.700
	if err := validate(in); err == nil {
		t.Fatal("lng without lat should be rejected, got nil error")
	}
}

func TestExcelRoundTripSeverityAndCoordinates(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Ho_Chi_Minh")
	if err != nil {
		loc = vnLocation
	}
	original := []*Incident{
		{
			Time: time.Date(2026, 3, 15, 8, 30, 0, 0, loc), Category: "Đông xe",
			Severity: 3, Content: "Ùn tắc kéo dài", Unit: "ĐỘI CSGT HÀNG XANH",
			Location: "Ngã tư Hàng Xanh", Lat: 10.8012, Lng: 106.7108,
		},
		{
			// No severity, no coordinates — must round-trip as zero/unset,
			// not e.g. become 0,0 (a real point in the Gulf of Guinea).
			Time: time.Date(2026, 3, 16, 14, 0, 0, 0, loc), Category: "Sự cố khác",
			Content: "Mất điện đèn tín hiệu", Unit: "PC08",
		},
	}

	var buf bytes.Buffer
	if err := WriteExcel(&buf, original); err != nil {
		t.Fatalf("WriteExcel: %v", err)
	}

	res, err := ImportExcel(bytes.NewReader(buf.Bytes()), "tester", true /* dryRun */)
	if err != nil {
		t.Fatalf("ImportExcel: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected import errors: %v", res.Errors)
	}
	if res.Imported != 2 {
		t.Fatalf("expected 2 rows imported (dry run), got %d (skipped=%d)", res.Imported, res.Skipped)
	}
}

func TestRowToIncidentParsesSeverityAndCoordinates(t *testing.T) {
	header := []string{"Thời gian", "Thể loại", "Mức độ", "Nội dung", "Đơn vị", "Vị trí", "Lat", "Lng"}
	col := mapHeaders(header)

	row := []string{"15/03/2026 08:30", "Va chạm", "2", "Va chạm nhẹ", "PC08", "Đường A", "10.776", "106.700"}
	in, err := rowToIncident(row, col)
	if err != nil {
		t.Fatalf("rowToIncident: %v", err)
	}
	if in.Severity != 2 {
		t.Errorf("expected Severity=2, got %d", in.Severity)
	}
	if in.Lat != 10.776 || in.Lng != 106.700 {
		t.Errorf("expected Lat=10.776 Lng=106.700, got Lat=%v Lng=%v", in.Lat, in.Lng)
	}
}

// TestActiveWindowToleratesClockSkew is the regression test for the map's
// "🚨 Traffic Event" layer appearing to delay newly-created incidents: an
// incident's Time is set by the browser that created it, so if its clock
// runs even a little ahead of the server's, the incident's Time lands
// after the server's own "now" and used to be excluded by handleActive's
// To-bound until the server's clock caught up. activeWindow's grace period
// must absorb that without letting a wildly wrong Time (a multi-hour typo,
// say) count as "active" indefinitely.
func TestActiveWindowToleratesClockSkew(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)

	from, to := activeWindow(now, 10)
	if !from.Equal(now.Add(-12 * time.Minute)) {
		t.Errorf("from = %v, want %v (10 min window + 2 min grace)", from, now.Add(-12*time.Minute))
	}
	if !to.Equal(now.Add(2 * time.Minute)) {
		t.Errorf("to = %v, want %v (2 min grace)", to, now.Add(2*time.Minute))
	}

	// A client clock 90s ahead of the server's must not get excluded.
	clientAheadTime := now.Add(90 * time.Second)
	if clientAheadTime.After(to) {
		t.Errorf("incident Time %v (90s client clock skew) should fall within [%v, %v]", clientAheadTime, from, to)
	}

	// 0 (admin hasn't configured a window) falls back to the 10-minute default.
	fromDefault, toDefault := activeWindow(now, 0)
	if !fromDefault.Equal(from) || !toDefault.Equal(to) {
		t.Errorf("windowMin=0 should behave like windowMin=10, got from=%v to=%v", fromDefault, toDefault)
	}

	// A Time hours in the future (a real typo, not clock skew) must still
	// fall outside the window — the grace period bounds the damage.
	wayFuture := now.Add(3 * time.Hour)
	if !wayFuture.After(to) {
		t.Errorf("incident Time %v (3h in the future) should fall outside the window (to=%v)", wayFuture, to)
	}
}

func TestRowToIncidentBlankSeverityAndCoordinatesStayZero(t *testing.T) {
	header := []string{"Thời gian", "Thể loại", "Mức độ", "Nội dung", "Đơn vị", "Lat", "Lng"}
	col := mapHeaders(header)

	row := []string{"15/03/2026 08:30", "Va chạm", "", "Va chạm nhẹ", "PC08", "", ""}
	in, err := rowToIncident(row, col)
	if err != nil {
		t.Fatalf("rowToIncident: %v", err)
	}
	if in.Severity != 0 {
		t.Errorf("expected Severity=0 (unset) for a blank cell, got %d", in.Severity)
	}
	if in.Lat != 0 || in.Lng != 0 {
		t.Errorf("expected Lat=0 Lng=0 for blank cells, got Lat=%v Lng=%v", in.Lat, in.Lng)
	}
}
