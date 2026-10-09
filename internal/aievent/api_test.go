package aievent

import "testing"

func validMinimalPush() *pushRequest {
	req := &pushRequest{Type: "event.created", RecordID: "r1"}
	req.Category.TypeID = 12
	req.Event.StartTime.Value = "2026-09-25T03:48:54Z"
	return req
}

func TestValidatePushAcceptsMinimalValidRequest(t *testing.T) {
	if err := validatePush(validMinimalPush()); err != nil {
		t.Errorf("expected no error, got %+v", err)
	}
}

func TestValidatePushAcceptsTerminated(t *testing.T) {
	req := validMinimalPush()
	req.Type = "event.terminated"
	if err := validatePush(req); err != nil {
		t.Errorf("expected no error, got %+v", err)
	}
}

func TestValidatePushRequiresRecordID(t *testing.T) {
	req := validMinimalPush()
	req.RecordID = ""
	err := validatePush(req)
	if err == nil || err.Field != "recordId" {
		t.Errorf("expected missing recordId error, got %+v", err)
	}
}

func TestValidatePushRejectsUnknownType(t *testing.T) {
	req := validMinimalPush()
	req.Type = "event.bogus"
	err := validatePush(req)
	if err == nil || err.Field != "type" {
		t.Errorf("expected invalid type error, got %+v", err)
	}
}

func TestValidatePushRequiresCategoryTypeID(t *testing.T) {
	req := validMinimalPush()
	req.Category.TypeID = 0
	err := validatePush(req)
	if err == nil || err.Field != "category.typeId" {
		t.Errorf("expected missing category.typeId error, got %+v", err)
	}
}

func TestValidatePushRequiresStartTime(t *testing.T) {
	req := validMinimalPush()
	req.Event.StartTime.Value = ""
	err := validatePush(req)
	if err == nil || err.Field != "event.StartTime.Value" {
		t.Errorf("expected missing StartTime error, got %+v", err)
	}
}

func TestValidatePushRejectsUnparsableStartTime(t *testing.T) {
	req := validMinimalPush()
	req.Event.StartTime.Value = "not-a-date"
	err := validatePush(req)
	if err == nil || err.Field != "event.StartTime.Value" || err.Code != "invalid_field_value" {
		t.Errorf("expected invalid_field_value for StartTime, got %+v", err)
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
