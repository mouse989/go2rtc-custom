package traffic

import (
	"testing"
	"time"
)

func resetFlowCaptureDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := initFlowCaptureDB(dir); err != nil {
		t.Fatalf("initFlowCaptureDB: %v", err)
	}
	t.Cleanup(func() {
		if flowCaptureDB != nil {
			_ = flowCaptureDB.Close()
			flowCaptureDB = nil
		}
	})
}

func TestRecordAndListFlowScan(t *testing.T) {
	resetFlowCaptureDB(t)

	results, sourceUpdated, err := parseFlowResults([]byte(realSampleJSON))
	if err != nil {
		t.Fatalf("parseFlowResults: %v", err)
	}
	scannedAt := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if err := recordFlowScan("TPHCM BD", scannedAt, sourceUpdated, len(realSampleJSON), []byte(realSampleJSON), results); err != nil {
		t.Fatalf("recordFlowScan: %v", err)
	}

	scans, err := listFlowScans(10)
	if err != nil {
		t.Fatalf("listFlowScans: %v", err)
	}
	if len(scans) != 1 {
		t.Fatalf("got %d scans, want 1", len(scans))
	}
	s := scans[0]
	if s.RegionName != "TPHCM BD" {
		t.Errorf("RegionName = %q, want %q", s.RegionName, "TPHCM BD")
	}
	if s.SourceUpdated != sourceUpdated {
		t.Errorf("SourceUpdated = %q, want %q", s.SourceUpdated, sourceUpdated)
	}
	if s.ResultCount != 2 {
		t.Errorf("ResultCount = %d, want 2", s.ResultCount)
	}

	segs, err := listFlowSegments(s.ID)
	if err != nil {
		t.Fatalf("listFlowSegments: %v", err)
	}
	// 2 top-level results (sub_index=-1) + 3 subSegments from the second
	// result = 5 rows total.
	if len(segs) != 5 {
		t.Fatalf("got %d segments, want 5 (2 results + 3 subSegments)", len(segs))
	}

	// First row: the top-level "no description" result.
	top0 := segs[0]
	if top0.SubIndex != -1 || top0.ResultIndex != 0 {
		t.Errorf("segs[0] (result,sub) = (%d,%d), want (0,-1)", top0.ResultIndex, top0.SubIndex)
	}
	if top0.JamFactor != 8.7 {
		t.Errorf("segs[0].JamFactor = %v, want 8.7", top0.JamFactor)
	}
	if top0.FunctionalClass == nil || *top0.FunctionalClass != 2 {
		t.Errorf("segs[0].FunctionalClass = %v, want 2", top0.FunctionalClass)
	}
	if top0.FirstLat == nil || *top0.FirstLat != 10.80114 {
		t.Errorf("segs[0].FirstLat = %v, want 10.80114", top0.FirstLat)
	}
	wantSpeedKmh := 1.9444445 * 3.6
	if diff := top0.SpeedKmh - wantSpeedKmh; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("segs[0].SpeedKmh = %v, want %v (speed_mps * 3.6)", top0.SpeedKmh, wantSpeedKmh)
	}

	// Second result (Duong Nguyen Thi Thap) + its 3 subSegments, in order.
	top1 := segs[1]
	if top1.Description != "Duong Nguyen Thi Thap" || top1.SubIndex != -1 {
		t.Errorf("segs[1] = %+v, want the Nguyen Thi Thap top-level row", top1)
	}
	sub0, sub1, sub2 := segs[2], segs[3], segs[4]
	for i, sub := range []FlowSegmentRow{sub0, sub1, sub2} {
		if sub.ResultIndex != 1 || sub.SubIndex != i {
			t.Errorf("subSegment %d: (result,sub) = (%d,%d), want (1,%d)", i, sub.ResultIndex, sub.SubIndex, i)
		}
	}
	if sub1.JamFactor != 3.9 {
		t.Errorf("sub1 (the relatively clear middle piece).JamFactor = %v, want 3.9", sub1.JamFactor)
	}
	if !sub1.HasJamTendency || sub1.JamTendency != 1 {
		t.Errorf("sub1 jamTendency = (has=%v, val=%v), want (true, 1)", sub1.HasJamTendency, sub1.JamTendency)
	}
	if sub0.HasJamTendency {
		t.Errorf("sub0.HasJamTendency = true, want false (field absent on this piece in the real sample)")
	}

	raw, err := rawFlowScanJSON(s.ID)
	if err != nil {
		t.Fatalf("rawFlowScanJSON: %v", err)
	}
	if raw != realSampleJSON {
		t.Error("rawFlowScanJSON did not return the verbatim raw JSON that was recorded")
	}
}

func TestListFlowScansEmptyWhenNothingRecorded(t *testing.T) {
	resetFlowCaptureDB(t)
	scans, err := listFlowScans(10)
	if err != nil {
		t.Fatalf("listFlowScans: %v", err)
	}
	if len(scans) != 0 {
		t.Errorf("got %d scans, want 0", len(scans))
	}
}

func TestRecordFlowScanSkipsWhenCaptureDBNotInitialized(t *testing.T) {
	flowCaptureDB = nil
	if err := recordFlowScan("x", time.Now(), "", 0, nil, nil); err != nil {
		t.Errorf("recordFlowScan with nil DB = %v, want nil (no-op)", err)
	}
}
