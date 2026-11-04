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

// resetIncidentsStore points the package's in-memory store at a fresh temp
// dir so tests don't collide with each other or the real data dir.
func resetIncidentsStore(t *testing.T) {
	t.Helper()
	mu.Lock()
	years = map[int]map[string]*Incident{}
	dataDir = t.TempDir()
	mu.Unlock()
}

// TestEndIncidentSetsEndTimeAndIsIdempotent is the regression test for the
// incidents.html table's green "✓" button: ending an incident must record
// EndTime (and who did it), and ending an already-ended incident again must
// not clobber the original EndTime (guards against a double-click/retry
// silently moving the recorded end time forward).
func TestEndIncidentSetsEndTimeAndIsIdempotent(t *testing.T) {
	resetIncidentsStore(t)
	in := baseIncident()
	in.Lat, in.Lng = 10.776, 106.700
	if err := Create(in, "creator"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	year := in.Time.Year()

	ended, err := EndIncident(year, in.ID, "closer")
	if err != nil {
		t.Fatalf("EndIncident: %v", err)
	}
	if ended.EndTime == nil {
		t.Fatal("EndTime should be set after EndIncident")
	}
	if ended.EndedBy != "closer" {
		t.Errorf("EndedBy = %q, want closer", ended.EndedBy)
	}
	firstEnd := *ended.EndTime

	// Ending again must be a no-op, not move the end time.
	again, err := EndIncident(year, in.ID, "someone-else")
	if err != nil {
		t.Fatalf("EndIncident (second call): %v", err)
	}
	if again.EndTime == nil || !again.EndTime.Equal(firstEnd) {
		t.Errorf("EndTime changed on second EndIncident call: %v -> %v", firstEnd, again.EndTime)
	}
	if again.EndedBy != "closer" {
		t.Errorf("EndedBy changed on second EndIncident call: got %q, want closer", again.EndedBy)
	}

	if _, err := EndIncident(year, "no-such-id", "x"); err != errNotFound {
		t.Errorf("EndIncident on unknown id = %v, want errNotFound", err)
	}
}

// TestActiveListIgnoresAgeOnlyEndTime is the regression test for the map's
// "🚨 Traffic Event" layer: unlike every other layer, an unresolved incident
// must keep showing however old it is, and must disappear the moment it's
// ended — there is no rolling time window anymore.
func TestActiveListIgnoresAgeOnlyEndTime(t *testing.T) {
	resetIncidentsStore(t)

	veryOld := baseIncident()
	veryOld.Time = time.Now().In(vnLocation).AddDate(-1, 0, 0)
	veryOld.Lat, veryOld.Lng = 10.1, 106.1
	if err := Create(veryOld, "u"); err != nil {
		t.Fatalf("Create (old, unresolved): %v", err)
	}

	ended := baseIncident()
	ended.Lat, ended.Lng = 10.2, 106.2
	if err := Create(ended, "u"); err != nil {
		t.Fatalf("Create (ended): %v", err)
	}
	if _, err := EndIncident(ended.Time.Year(), ended.ID, "u"); err != nil {
		t.Fatalf("EndIncident: %v", err)
	}

	noCoord := baseIncident()
	if err := Create(noCoord, "u"); err != nil {
		t.Fatalf("Create (no coord): %v", err)
	}

	list, err := ActiveList()
	if err != nil {
		t.Fatalf("ActiveList: %v", err)
	}
	ids := map[string]bool{}
	for _, in := range list {
		ids[in.ID] = true
	}
	if !ids[veryOld.ID] {
		t.Error("a year-old unresolved incident with coordinates should still be active")
	}
	if ids[ended.ID] {
		t.Error("an ended incident should not be active, regardless of recency")
	}
	if ids[noCoord.ID] {
		t.Error("an incident with no coordinates should never be active (nothing to place on the map)")
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
