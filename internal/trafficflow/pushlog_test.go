package trafficflow

import (
	"testing"
	"time"
)

func TestLogPushAttemptAndList(t *testing.T) {
	setupTestDB(t)

	logPushAttempt(PushLogEntry{RemoteIP: "203.0.113.1", AuthResult: "missing_api_key", HTTPStatus: 401})
	valid := true
	logPushAttempt(PushLogEntry{RemoteIP: "203.0.113.2", AuthResult: "ok", Integration: "Hawkwise", HTTPStatus: 200, Body: `{"general":{}}`, BodyValidJSON: &valid, SourceUUID: "cam-01"})

	entries := ListPushLog(PushLogFilter{})
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// Newest first.
	if entries[0].RemoteIP != "203.0.113.2" || entries[0].AuthResult != "ok" || entries[0].Integration != "Hawkwise" {
		t.Fatalf("unexpected newest entry: %+v", entries[0])
	}
	if entries[0].SourceUUID != "cam-01" {
		t.Fatalf("expected SourceUUID to round-trip, got %q", entries[0].SourceUUID)
	}
	if entries[0].BodyValidJSON == nil || !*entries[0].BodyValidJSON {
		t.Fatalf("expected BodyValidJSON=true on the successful entry, got %+v", entries[0].BodyValidJSON)
	}
	if entries[0].Body != `{"general":{}}` {
		t.Fatalf("expected body to round-trip, got %q", entries[0].Body)
	}
	if entries[1].RemoteIP != "203.0.113.1" || entries[1].BodyValidJSON != nil {
		t.Fatalf("expected the rejected entry to carry no body/validJSON, got %+v", entries[1])
	}
}

func TestListPushLogResolvesCurrentSensorMapping(t *testing.T) {
	setupTestDB(t)

	logPushAttempt(PushLogEntry{RemoteIP: "203.0.113.1", AuthResult: "ok", HTTPStatus: 200, SourceUUID: "cam-mapped"})
	logPushAttempt(PushLogEntry{RemoteIP: "203.0.113.1", AuthResult: "ok", HTTPStatus: 200, SourceUUID: "cam-unmapped"})

	entries := ListPushLog(PushLogFilter{})
	for _, e := range entries {
		if e.SensorLabel != "" {
			t.Fatalf("expected no label before any Sensor exists, got %+v", e)
		}
	}

	if _, err := CreateSensor(Sensor{SourceUUID: "cam-mapped", Label: "Ngã tư ABC", Lat: 10.7, Lon: 106.7, Enabled: true}); err != nil {
		t.Fatalf("CreateSensor: %v", err)
	}

	// A mapping created AFTER the push already arrived must still resolve
	// against that past entry — the join happens at query time, not at
	// insert time.
	entries = ListPushLog(PushLogFilter{})
	byUUID := map[string]PushLogEntry{}
	for _, e := range entries {
		byUUID[e.SourceUUID] = e
	}
	if byUUID["cam-mapped"].SensorLabel != "Ngã tư ABC" {
		t.Fatalf("expected cam-mapped to resolve to its Sensor label, got %+v", byUUID["cam-mapped"])
	}
	if byUUID["cam-unmapped"].SensorLabel != "" {
		t.Fatalf("expected cam-unmapped to have no label, got %+v", byUUID["cam-unmapped"])
	}
}

func TestListPushLogFiltersByTimeRange(t *testing.T) {
	setupTestDB(t)

	old := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	recent := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339Nano)
	logPushAttempt(PushLogEntry{ReceivedAt: old, RemoteIP: "203.0.113.1", AuthResult: "ok", HTTPStatus: 200, SourceUUID: "cam-old"})
	logPushAttempt(PushLogEntry{ReceivedAt: recent, RemoteIP: "203.0.113.1", AuthResult: "ok", HTTPStatus: 200, SourceUUID: "cam-recent"})

	all := ListPushLog(PushLogFilter{})
	if len(all) != 2 {
		t.Fatalf("expected 2 entries unfiltered, got %d", len(all))
	}

	onlyRecent := ListPushLog(PushLogFilter{From: time.Now().Add(-30 * time.Minute)})
	if len(onlyRecent) != 1 || onlyRecent[0].SourceUUID != "cam-recent" {
		t.Fatalf("expected only the recent entry with From=-30m, got %+v", onlyRecent)
	}

	onlyOld := ListPushLog(PushLogFilter{To: time.Now().Add(-1 * time.Hour)})
	if len(onlyOld) != 1 || onlyOld[0].SourceUUID != "cam-old" {
		t.Fatalf("expected only the old entry with To=-1h, got %+v", onlyOld)
	}
}

func TestLogPushAttemptPrunesToMaxRows(t *testing.T) {
	setupTestDB(t)

	for i := 0; i < pushLogMaxRows+10; i++ {
		logPushAttempt(PushLogEntry{RemoteIP: "203.0.113.1", AuthResult: "missing_api_key", HTTPStatus: 401})
	}
	entries := ListPushLog(PushLogFilter{})
	if len(entries) != pushLogMaxRows {
		t.Fatalf("expected pruning to cap at %d rows, got %d", pushLogMaxRows, len(entries))
	}
}

func TestLogPushAttemptTruncatesOversizedBody(t *testing.T) {
	setupTestDB(t)

	big := make([]byte, pushLogBodyMaxChars+500)
	for i := range big {
		big[i] = 'x'
	}
	valid := true
	logPushAttempt(PushLogEntry{RemoteIP: "203.0.113.1", AuthResult: "ok", HTTPStatus: 200, Body: string(big), BodyValidJSON: &valid})

	entries := ListPushLog(PushLogFilter{})
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if !entries[0].BodyTruncated {
		t.Fatalf("expected BodyTruncated=true for an oversized body")
	}
	if len(entries[0].Body) != pushLogBodyMaxChars {
		t.Fatalf("expected stored body capped at %d chars, got %d", pushLogBodyMaxChars, len(entries[0].Body))
	}
}

func TestClearPushLogRemovesEverything(t *testing.T) {
	setupTestDB(t)

	logPushAttempt(PushLogEntry{RemoteIP: "203.0.113.1", AuthResult: "missing_api_key", HTTPStatus: 401})
	logPushAttempt(PushLogEntry{RemoteIP: "203.0.113.2", AuthResult: "missing_api_key", HTTPStatus: 401})
	if len(ListPushLog(PushLogFilter{})) != 2 {
		t.Fatalf("setup: expected 2 entries before clearing")
	}
	if err := ClearPushLog(); err != nil {
		t.Fatalf("ClearPushLog: %v", err)
	}
	if entries := ListPushLog(PushLogFilter{}); len(entries) != 0 {
		t.Fatalf("expected 0 entries after ClearPushLog, got %d", len(entries))
	}
}
