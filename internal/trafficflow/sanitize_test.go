package trafficflow

import (
	"encoding/json"
	"testing"
)

func TestSanitizeNonFiniteJSONReturnsNilWhenNothingToFix(t *testing.T) {
	body := []byte(`{"avg_speed": 5, "label": "car"}`)
	if got := sanitizeNonFiniteJSON(body); got != nil {
		t.Errorf("expected nil (nothing to sanitize), got %q", got)
	}
}

func TestSanitizeNonFiniteJSONReplacesBareNaN(t *testing.T) {
	body := []byte(`{"avg_speed": NaN, "volume": 0}`)
	got := sanitizeNonFiniteJSON(body)
	if got == nil {
		t.Fatal("expected a sanitized copy, got nil")
	}
	want := `{"avg_speed": 0, "volume": 0}`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSanitizeNonFiniteJSONReplacesInfinityAndNegativeInfinity(t *testing.T) {
	body := []byte(`{"a": Infinity, "b": -Infinity, "c": [Infinity, 1]}`)
	got := sanitizeNonFiniteJSON(body)
	want := `{"a": 0, "b": 0, "c": [0, 1]}`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSanitizeNonFiniteJSONIgnoresTokenInsideString(t *testing.T) {
	body := []byte(`{"note": "status is NaN today"}`)
	if got := sanitizeNonFiniteJSON(body); got != nil {
		t.Errorf("expected nil — a quoted NaN inside a string is legal JSON and must not be touched, got %q", got)
	}
}

func TestSanitizeNonFiniteJSONProducesParsableJSON(t *testing.T) {
	// The real-world shape that motivated this: a vehicle class with 0
	// volume gets avg_speed computed as 0/0 by the camera and serialized
	// as a bare NaN, which breaks the whole payload otherwise.
	body := []byte(`{
		"general": {"camera": {"source_uuid": "cam-xyz"}},
		"details": {"density_detail": {"data": [
			{"label": "bus", "volume": 0, "avg_speed": NaN},
			{"label": "car", "volume": 5, "avg_speed": 6}
		]}}
	}`)
	cleaned := sanitizeNonFiniteJSON(body)
	if cleaned == nil {
		t.Fatal("expected sanitization to trigger")
	}
	var req hawkwisePush
	if err := json.Unmarshal(cleaned, &req); err != nil {
		t.Fatalf("sanitized body should parse cleanly, got error: %v", err)
	}
	if req.General.Camera.SourceUUID != "cam-xyz" {
		t.Errorf("expected source_uuid to survive sanitization, got %q", req.General.Camera.SourceUUID)
	}
}

func TestBestEffortSourceUUIDFindsValue(t *testing.T) {
	body := []byte(`[{"general":{"camera":{"source_uuid":"b8a0cf41-d1f0-4bc3-b242-4bdc937d148e"}}}]`)
	if got := bestEffortSourceUUID(body); got != "b8a0cf41-d1f0-4bc3-b242-4bdc937d148e" {
		t.Errorf("got %q", got)
	}
}

func TestBestEffortSourceUUIDEmptyWhenAbsent(t *testing.T) {
	body := []byte(`{"general":{"event_id":123}}`)
	if got := bestEffortSourceUUID(body); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestParseHawkwisePushSucceedsOnCleanBody(t *testing.T) {
	body := []byte(`{"general":{"camera":{"source_uuid":"cam-1"}}}`)
	req, err, recovered := parseHawkwisePush(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recovered {
		t.Error("expected recovered=false for a clean body")
	}
	if req.General.Camera.SourceUUID != "cam-1" {
		t.Errorf("got source_uuid %q", req.General.Camera.SourceUUID)
	}
}

// This is the exact bug this file fixes: a push whose body is otherwise
// perfectly valid — source_uuid included — used to fail json.Unmarshal
// outright because of one bare NaN token, losing the source_uuid from the
// push log (and the record itself from ingestion) even though the camera
// was correctly identified and mapped.
func TestParseHawkwisePushRecoversFromBareNaN(t *testing.T) {
	body := []byte(`{
		"general": {"event_id": 1, "camera": {"source_uuid": "b8a0cf41-d1f0-4bc3-b242-4bdc937d148e"}},
		"details": {"density_detail": {"data": [{"label": "bus", "volume": 0, "avg_speed": NaN}]}}
	}`)
	req, err, recovered := parseHawkwisePush(body)
	if err != nil {
		t.Fatalf("expected recovery to succeed, got error: %v", err)
	}
	if !recovered {
		t.Error("expected recovered=true")
	}
	if req.General.Camera.SourceUUID != "b8a0cf41-d1f0-4bc3-b242-4bdc937d148e" {
		t.Errorf("expected source_uuid to survive recovery, got %q", req.General.Camera.SourceUUID)
	}
}

func TestParseHawkwisePushReturnsOriginalErrorWhenUnrecoverable(t *testing.T) {
	body := []byte(`{this is not json at all`)
	_, err, recovered := parseHawkwisePush(body)
	if err == nil {
		t.Fatal("expected an error for genuinely malformed JSON")
	}
	if recovered {
		t.Error("expected recovered=false")
	}
}
