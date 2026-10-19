package trafficflow

import "testing"

func TestLogPushAttemptAndList(t *testing.T) {
	setupTestDB(t)

	logPushAttempt(PushLogEntry{RemoteIP: "203.0.113.1", AuthResult: "missing_api_key", HTTPStatus: 401})
	valid := true
	logPushAttempt(PushLogEntry{RemoteIP: "203.0.113.2", AuthResult: "ok", Integration: "Hawkwise", HTTPStatus: 200, Body: `{"general":{}}`, BodyValidJSON: &valid})

	entries := ListPushLog()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// Newest first.
	if entries[0].RemoteIP != "203.0.113.2" || entries[0].AuthResult != "ok" || entries[0].Integration != "Hawkwise" {
		t.Fatalf("unexpected newest entry: %+v", entries[0])
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

func TestLogPushAttemptPrunesToMaxRows(t *testing.T) {
	setupTestDB(t)

	for i := 0; i < pushLogMaxRows+10; i++ {
		logPushAttempt(PushLogEntry{RemoteIP: "203.0.113.1", AuthResult: "missing_api_key", HTTPStatus: 401})
	}
	entries := ListPushLog()
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

	entries := ListPushLog()
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
	if len(ListPushLog()) != 2 {
		t.Fatalf("setup: expected 2 entries before clearing")
	}
	if err := ClearPushLog(); err != nil {
		t.Fatalf("ClearPushLog: %v", err)
	}
	if entries := ListPushLog(); len(entries) != 0 {
		t.Fatalf("expected 0 entries after ClearPushLog, got %d", len(entries))
	}
}
