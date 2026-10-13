package trafficflow

import "testing"

func validMinimalPush() *hawkwisePush {
	var req hawkwisePush
	req.General.EventID = 482558
	req.General.Camera.SourceUUID = "cam-1"
	req.General.StartTime = "2026-10-13 08:00:00"
	req.General.EndTime = "2026-10-13 08:05:00"
	return &req
}

func TestValidatePushAcceptsMinimalValidRequest(t *testing.T) {
	if err := validatePush(validMinimalPush()); err != nil {
		t.Errorf("expected no error, got %+v", err)
	}
}

func TestValidatePushRequiresEventID(t *testing.T) {
	req := validMinimalPush()
	req.General.EventID = 0
	err := validatePush(req)
	if err == nil || err.Field != "general.event_id" {
		t.Errorf("expected missing event_id error, got %+v", err)
	}
}

func TestValidatePushRequiresSourceUUID(t *testing.T) {
	req := validMinimalPush()
	req.General.Camera.SourceUUID = ""
	err := validatePush(req)
	if err == nil || err.Field != "general.camera.source_uuid" {
		t.Errorf("expected missing source_uuid error, got %+v", err)
	}
}

func TestValidatePushRequiresStartTime(t *testing.T) {
	req := validMinimalPush()
	req.General.StartTime = ""
	err := validatePush(req)
	if err == nil || err.Field != "general.start_time" {
		t.Errorf("expected missing start_time error, got %+v", err)
	}
}

func TestValidatePushRejectsUnparsableStartTime(t *testing.T) {
	req := validMinimalPush()
	req.General.StartTime = "not-a-date"
	err := validatePush(req)
	if err == nil || err.Field != "general.start_time" || err.Code != "invalid_field_value" {
		t.Errorf("expected invalid_field_value for start_time, got %+v", err)
	}
}

// Hawkwise sends local "yyyy-MM-dd HH:mm:ss" with NO zone suffix — an
// RFC3339 string must be rejected, since accepting it would silently imply
// UTC via a different code path than parseHawkwiseTime (the exact class of
// bug that originally hit internal/incidents' date filter).
func TestValidatePushRejectsRFC3339StartTime(t *testing.T) {
	req := validMinimalPush()
	req.General.StartTime = "2026-10-13T08:00:00Z"
	err := validatePush(req)
	if err == nil || err.Field != "general.start_time" {
		t.Errorf("expected general.start_time to reject an RFC3339 string, got %+v", err)
	}
}

func TestValidatePushRequiresEndTime(t *testing.T) {
	req := validMinimalPush()
	req.General.EndTime = ""
	err := validatePush(req)
	if err == nil || err.Field != "general.end_time" {
		t.Errorf("expected missing end_time error, got %+v", err)
	}
}

func TestValidatePushAcceptsUnknownTriggerCode(t *testing.T) {
	req := validMinimalPush()
	req.General.TriggerCode = "SomeFutureAIFlow"
	if err := validatePush(req); err != nil {
		t.Errorf("an unrecognized trigger_code must not be rejected (plan §2.4), got %+v", err)
	}
}

func TestCheckPushRateLimitAllowsUpToLimitThenDenies(t *testing.T) {
	rateMu.Lock()
	rateState = map[string]*rateWindow{}
	rateMu.Unlock()

	id := "integration-x"
	for i := 0; i < pushRateLimitPerMinute; i++ {
		if allowed, _ := checkPushRateLimit(id); !allowed {
			t.Fatalf("request %d should be allowed (limit %d)", i, pushRateLimitPerMinute)
		}
	}
	allowed, retry := checkPushRateLimit(id)
	if allowed {
		t.Error("expected the request beyond the limit to be denied")
	}
	if retry <= 0 {
		t.Errorf("expected a positive retry-after, got %d", retry)
	}
}

func TestCheckPushRateLimitTracksIntegrationsIndependently(t *testing.T) {
	rateMu.Lock()
	rateState = map[string]*rateWindow{}
	rateMu.Unlock()

	for i := 0; i < pushRateLimitPerMinute; i++ {
		checkPushRateLimit("integration-a")
	}
	if allowed, _ := checkPushRateLimit("integration-a"); allowed {
		t.Error("expected integration-a to be rate-limited")
	}
	if allowed, _ := checkPushRateLimit("integration-b"); !allowed {
		t.Error("expected integration-b to be unaffected by integration-a's rate limit")
	}
}
