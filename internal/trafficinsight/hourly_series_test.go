package trafficinsight

import (
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/traveltime"
)

func TestGetHourlySeriesAlwaysReturns24HoursInOrder(t *testing.T) {
	setupSources(t)
	series, err := GetHourlySeries("2027-05-01")
	if err != nil {
		t.Fatalf("GetHourlySeries: %v", err)
	}
	if len(series) != 24 {
		t.Fatalf("expected 24 hourly buckets, got %d", len(series))
	}
	for h, m := range series {
		if m.Hour != h {
			t.Fatalf("bucket %d has Hour=%d, want %d", h, m.Hour, h)
		}
		if m.HasJamData || m.HasTTIData || m.IncidentCount != 0 {
			t.Errorf("hour %d: expected no data on a date with no fixtures, got %+v", h, m)
		}
	}
}

func TestGetHourlySeriesBucketsByHourAcrossAllThreeSources(t *testing.T) {
	date := "2027-05-02"
	setupSources(t)

	writeTrafficDay(t, date, []fixtureScan{
		scanAtVN(2027, 5, 2, 8, 0, []fixturePersistentPoint{
			{Lat: 10.1, Lng: 106.1, JamFactor: 6},
			{Lat: 10.2, Lng: 106.2, JamFactor: 8},
		}),
		scanAtVN(2027, 5, 2, 8, 30, []fixturePersistentPoint{ // same hour (8), different scan
			{Lat: 10.1, Lng: 106.1, JamFactor: 4},
		}),
		scanAtVN(2027, 5, 2, 17, 0, []fixturePersistentPoint{ // different hour (17)
			{Lat: 10.1, Lng: 106.1, JamFactor: 10},
		}),
	})

	writeTravelTimeDay(t, date, []traveltime.LogEntry{
		{Timestamp: date + "T08:15:00+07:00", RouteID: "r1", TTI: 1.5, DelaySec: 90},
		{Timestamp: date + "T08:45:00+07:00", RouteID: "r1", TTI: 2.5, DelaySec: 150}, // same hour (8)
		{Timestamp: date + "T17:30:00+07:00", RouteID: "r1", TTI: 3.0, DelaySec: 300}, // different hour (17)
	})

	createIncident(t, time.Date(2027, 5, 2, 8, 10, 0, 0, vnLocation), "Va chạm")
	createIncident(t, time.Date(2027, 5, 2, 8, 50, 0, 0, vnLocation), "Va chạm")
	createIncident(t, time.Date(2027, 5, 2, 20, 0, 0, 0, vnLocation), "Đông xe")

	series, err := GetHourlySeries(date)
	if err != nil {
		t.Fatalf("GetHourlySeries: %v", err)
	}

	hour8 := series[8]
	if !hour8.HasJamData || hour8.JamPointCount != 3 {
		t.Fatalf("hour 8: expected 3 jam points, got hasData=%v count=%d", hour8.HasJamData, hour8.JamPointCount)
	}
	wantJamAvg := (6.0 + 8.0 + 4.0) / 3.0
	if diff := hour8.JamAvgFactor - wantJamAvg; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("hour 8 JamAvgFactor = %v, want %v", hour8.JamAvgFactor, wantJamAvg)
	}
	if !hour8.HasTTIData || hour8.TTISampleCount != 2 {
		t.Fatalf("hour 8: expected 2 TTI samples, got hasData=%v count=%d", hour8.HasTTIData, hour8.TTISampleCount)
	}
	wantTTIAvg := (1.5 + 2.5) / 2.0
	if diff := hour8.TTIAvg - wantTTIAvg; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("hour 8 TTIAvg = %v, want %v", hour8.TTIAvg, wantTTIAvg)
	}
	if hour8.IncidentCount != 2 {
		t.Errorf("hour 8 IncidentCount = %d, want 2", hour8.IncidentCount)
	}

	hour17 := series[17]
	if !hour17.HasJamData || hour17.JamPointCount != 1 || hour17.JamAvgFactor != 10 {
		t.Errorf("hour 17: expected 1 jam point of 10, got %+v", hour17)
	}
	if !hour17.HasTTIData || hour17.TTISampleCount != 1 || hour17.TTIAvg != 3.0 {
		t.Errorf("hour 17: expected 1 TTI sample of 3.0, got %+v", hour17)
	}

	hour20 := series[20]
	if hour20.IncidentCount != 1 {
		t.Errorf("hour 20 IncidentCount = %d, want 1", hour20.IncidentCount)
	}
	if hour20.HasJamData || hour20.HasTTIData {
		t.Errorf("hour 20: expected no jam/TTI data, got %+v", hour20)
	}

	// An hour with nothing at all must stay zero-valued, not error.
	hour3 := series[3]
	if hour3.HasJamData || hour3.HasTTIData || hour3.IncidentCount != 0 {
		t.Errorf("hour 3: expected no data, got %+v", hour3)
	}
}

func TestGetHourlySeriesExcludesNonPositiveTTI(t *testing.T) {
	date := "2027-05-03"
	setupSources(t)

	writeTravelTimeDay(t, date, []traveltime.LogEntry{
		{Timestamp: date + "T09:00:00+07:00", RouteID: "r1", TTI: 2.0, DelaySec: 120},
		{Timestamp: date + "T09:30:00+07:00", RouteID: "r1", TTI: 0, DelaySec: 0},   // no data that scan — excluded
		{Timestamp: date + "T09:45:00+07:00", RouteID: "", TTI: 5.0, DelaySec: 300}, // missing route id — excluded
	})

	series, err := GetHourlySeries(date)
	if err != nil {
		t.Fatalf("GetHourlySeries: %v", err)
	}
	hour9 := series[9]
	if !hour9.HasTTIData || hour9.TTISampleCount != 1 || hour9.TTIAvg != 2.0 {
		t.Errorf("hour 9: expected exactly 1 valid TTI sample of 2.0, got %+v", hour9)
	}
}

func TestGetHourlySeriesInvalidDateReturnsError(t *testing.T) {
	setupSources(t)
	if _, err := GetHourlySeries("not-a-date"); err == nil {
		t.Fatal("expected error for invalid date")
	}
}
