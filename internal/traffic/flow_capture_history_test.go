package traffic

import (
	"testing"
	"time"
)

func TestValidateHistoryRange(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		from, to time.Time
		wantErr  bool
	}{
		{"zero from", time.Time{}, now, true},
		{"zero to", now, time.Time{}, true},
		{"to before from", now, now.Add(-time.Hour), true},
		{"span too wide", now, now.AddDate(0, 0, maxFlowHistoryRangeDays+1), true},
		{"valid 1 day", now, now.AddDate(0, 0, 1), false},
		{"valid exactly at cap", now, now.Add(maxFlowHistoryRangeDays * 24 * time.Hour), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateHistoryRange(c.from, c.to)
			if (err != nil) != c.wantErr {
				t.Errorf("validateHistoryRange(%v, %v) err=%v, wantErr=%v", c.from, c.to, err, c.wantErr)
			}
		})
	}
}

func TestScanNearTimePerRegionPicksMostRecentAtOrBeforeTarget(t *testing.T) {
	resetFlowCaptureDB(t)
	setTestRegions(t, []Region{
		{Name: "Quận 1", Enabled: true},
		{Name: "Quận 3", Enabled: true},
	})

	t1 := time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

	seedFlowScan(t, "Quận 1", "Đường A", t1, 2.0, 8.0, 8.5, 10.80, 106.70, 10.81, 106.71)
	seedFlowScan(t, "Quận 1", "Đường A", t2, 2.0, 8.0, 8.5, 10.80, 106.70, 10.81, 106.71)
	seedFlowScan(t, "Quận 1", "Đường A", t3, 2.0, 8.0, 8.5, 10.80, 106.70, 10.81, 106.71)
	seedFlowScan(t, "Quận 3", "Đường B", t2, 7.5, 8.0, 2.0, 10.90, 106.80, 10.91, 106.81)

	scans, err := scanNearTimePerRegion(t2)
	if err != nil {
		t.Fatalf("scanNearTimePerRegion: %v", err)
	}
	if len(scans) != 2 {
		t.Fatalf("expected 2 regions, got %d: %+v", len(scans), scans)
	}
	byRegion := map[string]FlowScanSummary{}
	for _, s := range scans {
		byRegion[s.RegionName] = s
	}
	wantQ1 := t2.Format(time.RFC3339)
	if byRegion["Quận 1"].ScannedAt != wantQ1 {
		t.Errorf("Quận 1 scannedAt = %q, want %q (not t3, which is after target)", byRegion["Quận 1"].ScannedAt, wantQ1)
	}
	if byRegion["Quận 3"].ScannedAt != wantQ1 {
		t.Errorf("Quận 3 scannedAt = %q, want %q", byRegion["Quận 3"].ScannedAt, wantQ1)
	}

	// Before any data exists at all: no rows for either region.
	before, err := scanNearTimePerRegion(t1.Add(-time.Hour))
	if err != nil {
		t.Fatalf("scanNearTimePerRegion (before any data): %v", err)
	}
	if len(before) != 0 {
		t.Errorf("expected no scans before any data exists, got %+v", before)
	}
}

func TestListFlowRoadsExcludesUnnamedAndOutOfRange(t *testing.T) {
	resetFlowCaptureDB(t)

	base := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	seedFlowScan(t, "Quận 1", "Đường A", base, 2.0, 8.0, 8.5, 10.80, 106.70, 10.81, 106.71)
	seedFlowScan(t, "Quận 1", "", base, 2.0, 8.0, 8.5, 10.82, 106.72, 10.83, 106.73) // unnamed — excluded
	seedFlowScan(t, "Quận 1", "Đường C (ngoài khoảng)", base.AddDate(0, 0, 10), 2.0, 8.0, 8.5, 10.84, 106.74, 10.85, 106.75)

	roads, err := listFlowRoads(base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("listFlowRoads: %v", err)
	}
	if len(roads) != 1 || roads[0].Description != "Đường A" {
		t.Fatalf("expected only Đường A in range, got %+v", roads)
	}

	if _, err := listFlowRoads(time.Time{}, base); err == nil {
		t.Error("expected error for zero from, got nil")
	}
}

func TestComputeRoadHourlyStatsFindsPeakHourAndRespectsLevels(t *testing.T) {
	resetFlowCaptureDB(t)

	from := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)

	// 7h VN-local (00:00 UTC): congested (LOS reaches D/E/F range).
	seedFlowScan(t, "Quận 1", "Đường A", time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		2.0, 8.0, 8.5, 10.80, 106.70, 10.81, 106.71)
	// 14h VN-local (07:00 UTC): free-flowing, never congested.
	seedFlowScan(t, "Quận 1", "Đường A", time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC),
		7.5, 8.0, 2.0, 10.80, 106.70, 10.81, 106.71)
	// A second, worse 7h VN-local reading — same hour bucket as the first.
	seedFlowScan(t, "Quận 1", "Đường A", time.Date(2026, 11, 20, 0, 30, 0, 0, time.UTC),
		1.0, 8.0, 9.5, 10.80, 106.70, 10.81, 106.71)

	report, err := computeRoadHourlyStats("Quận 1", "Đường A", from, to, []string{"D", "E", "F"})
	if err != nil {
		t.Fatalf("computeRoadHourlyStats: %v", err)
	}
	if len(report.Hours) != 24 {
		t.Fatalf("expected 24 hour buckets, got %d", len(report.Hours))
	}
	if report.PeakHour != 7 {
		t.Errorf("PeakHour = %d, want 7 (00:00/00:30 UTC == 07:00 VN local)", report.PeakHour)
	}
	h7 := report.Hours[7]
	if h7.Count != 2 {
		t.Errorf("hour 7: Count = %d, want 2", h7.Count)
	}
	if h7.WorstCount != 2 {
		t.Errorf("hour 7: WorstCount = %d, want 2 (both readings congested)", h7.WorstCount)
	}
	if h7.WorstRate != 1.0 {
		t.Errorf("hour 7: WorstRate = %v, want 1.0", h7.WorstRate)
	}
	h14 := report.Hours[14]
	if h14.Count != 1 || h14.WorstCount != 0 {
		t.Errorf("hour 14: Count=%d WorstCount=%d, want 1/0 (free-flowing)", h14.Count, h14.WorstCount)
	}

	// Out-of-range validation still enforced here too.
	if _, err := computeRoadHourlyStats("Quận 1", "Đường A", to, from, []string{"D"}); err == nil {
		t.Error("expected error for to-before-from, got nil")
	}
	if _, err := computeRoadHourlyStats("Quận 1", "", from, to, []string{"D"}); err == nil {
		t.Error("expected error for empty description, got nil")
	}
}
