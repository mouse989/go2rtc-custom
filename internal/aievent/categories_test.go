package aievent

import "testing"

func setupCategoryTest(t *testing.T) {
	t.Helper()
	if err := initCategories(t.TempDir()); err != nil {
		t.Fatalf("initCategories: %v", err)
	}
}

func TestResolveIconExactMatch(t *testing.T) {
	setupCategoryTest(t)
	sub := 41
	if _, err := CreateCategoryRule(CategorySourceOMNIA, 12, &sub, "Xe sự cố", "xesc"); err != nil {
		t.Fatal(err)
	}
	label, icon, matched := ResolveIcon(CategorySourceOMNIA, 12, 41)
	if !matched || label != "Xe sự cố" || icon != "xesc" {
		t.Errorf("got label=%q icon=%q matched=%v", label, icon, matched)
	}
}

func TestResolveIconWildcardSubtype(t *testing.T) {
	setupCategoryTest(t)
	if _, err := CreateCategoryRule(CategorySourceOMNIA, 12, nil, "Sự cố chung", "dong"); err != nil {
		t.Fatal(err)
	}
	label, icon, matched := ResolveIcon(CategorySourceOMNIA, 12, 999) // subtype never explicitly registered
	if !matched || label != "Sự cố chung" || icon != "dong" {
		t.Errorf("got label=%q icon=%q matched=%v", label, icon, matched)
	}
}

func TestResolveIconExactBeatsWildcard(t *testing.T) {
	setupCategoryTest(t)
	if _, err := CreateCategoryRule(CategorySourceOMNIA, 12, nil, "Chung", "dong"); err != nil {
		t.Fatal(err)
	}
	sub := 41
	if _, err := CreateCategoryRule(CategorySourceOMNIA, 12, &sub, "Cụ thể", "xesc"); err != nil {
		t.Fatal(err)
	}

	label, icon, matched := ResolveIcon(CategorySourceOMNIA, 12, 41)
	if !matched || label != "Cụ thể" || icon != "xesc" {
		t.Errorf("exact rule should win over wildcard, got label=%q icon=%q matched=%v", label, icon, matched)
	}

	// A different subtype of the same typeId falls back to the wildcard rule.
	label2, icon2, matched2 := ResolveIcon(CategorySourceOMNIA, 12, 7)
	if !matched2 || label2 != "Chung" || icon2 != "dong" {
		t.Errorf("expected wildcard match for an unlisted subtype, got label=%q icon=%q matched=%v", label2, icon2, matched2)
	}
}

func TestResolveIconNoMatch(t *testing.T) {
	setupCategoryTest(t)
	_, _, matched := ResolveIcon(CategorySourceOMNIA, 999, 999)
	if matched {
		t.Error("expected no match for an unconfigured category")
	}
}

// Icon is free text (a Traffic Event category name to borrow artwork from,
// same as internal/incidents' own Category field) — any string is valid,
// including one with nothing configured for it yet on the Traffic Event
// side (falls back to the generic marker at render time, never rejected
// here).
func TestCreateCategoryRuleAcceptsArbitraryIconName(t *testing.T) {
	setupCategoryTest(t)
	if _, err := CreateCategoryRule(CategorySourceOMNIA, 1, nil, "Test", "Không tồn tại ở Sự cố GT"); err != nil {
		t.Errorf("expected an arbitrary icon name to be accepted, got %v", err)
	}
}

func TestCreateCategoryRuleRejectsEmptyLabel(t *testing.T) {
	setupCategoryTest(t)
	if _, err := CreateCategoryRule(CategorySourceOMNIA, 1, nil, "", "dong"); err == nil {
		t.Error("expected an error for an empty label")
	}
}

func TestDeleteCategoryRuleRemovesMatch(t *testing.T) {
	setupCategoryTest(t)
	rule, err := CreateCategoryRule(CategorySourceOMNIA, 1, nil, "Test", "dong")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteCategoryRule(rule.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, matched := ResolveIcon(CategorySourceOMNIA, 1, 1); matched {
		t.Error("expected no match after the rule was deleted")
	}
}

func TestObserveCategoryTracksCountAndLatestSample(t *testing.T) {
	setupCategoryTest(t)
	ObserveCategory(CategorySourceOMNIA, 12, 41, "VehicleObstruction", "first desc", "")
	ObserveCategory(CategorySourceOMNIA, 12, 41, "VehicleObstruction", "second desc", "")
	list := ListObservedCategories()
	if len(list) != 1 {
		t.Fatalf("expected 1 observed category, got %d", len(list))
	}
	if list[0].Count != 2 {
		t.Errorf("expected Count=2, got %d", list[0].Count)
	}
	if list[0].Description != "second desc" {
		t.Errorf("expected Description to be the most recent sample, got %q", list[0].Description)
	}
}

func TestObserveCategoryDistinguishesBySubtype(t *testing.T) {
	setupCategoryTest(t)
	ObserveCategory(CategorySourceOMNIA, 12, 41, "A", "", "")
	ObserveCategory(CategorySourceOMNIA, 12, 42, "B", "", "")
	list := ListObservedCategories()
	if len(list) != 2 {
		t.Errorf("expected 2 distinct observed categories (different subtypeId), got %d", len(list))
	}
}

// A Hawkwise ai_flow_code and an OMNIA typeId that happen to share the same
// number must never collide into the same rule/icon or the same observed
// entry — each vendor assigns its own numbering independently. See
// categories.go's categorySource consts doc comment.
func TestCategorySourceKeepsSameNumberFromDifferentVendorsApart(t *testing.T) {
	setupCategoryTest(t)
	if _, err := CreateCategoryRule(CategorySourceOMNIA, 21, nil, "OMNIA loại 21", "dong"); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateCategoryRule(CategorySourceHawkwise, 21, nil, "Hawkwise Abnormal Stop", "xesc"); err != nil {
		t.Fatal(err)
	}

	label, icon, matched := ResolveIcon(CategorySourceOMNIA, 21, 0)
	if !matched || label != "OMNIA loại 21" || icon != "dong" {
		t.Errorf("OMNIA lookup got label=%q icon=%q matched=%v, want the OMNIA rule", label, icon, matched)
	}
	label2, icon2, matched2 := ResolveIcon(CategorySourceHawkwise, 21, 0)
	if !matched2 || label2 != "Hawkwise Abnormal Stop" || icon2 != "xesc" {
		t.Errorf("Hawkwise lookup got label=%q icon=%q matched=%v, want the Hawkwise rule", label2, icon2, matched2)
	}

	ObserveCategory(CategorySourceOMNIA, 21, 0, "SomeRecordType", "omnia sample", "")
	ObserveCategory(CategorySourceHawkwise, 21, 0, "AbnormalStop", "Abnormal Stop", `{"name":"98A27706"}`)
	list := ListObservedCategories()
	if len(list) != 2 {
		t.Fatalf("expected 2 distinct observed entries (same number, different source), got %d", len(list))
	}
}

// Hawkwise entries carry a raw sample payload (the fields whose meaning
// isn't confirmed yet) so an admin can inspect it on the GUI — see
// docs/hawkwise-ai-flow-events-plan.md §5.
func TestObserveCategoryKeepsSampleRawJSON(t *testing.T) {
	setupCategoryTest(t)
	ObserveCategory(CategorySourceHawkwise, 21, 0, "AbnormalStop", "Abnormal Stop", `{"event_type":2}`)
	ObserveCategory(CategorySourceHawkwise, 21, 0, "AbnormalStop", "Abnormal Stop", `{"event_type":2,"object_action":0}`)
	list := ListObservedCategories()
	if len(list) != 1 {
		t.Fatalf("expected 1 observed category, got %d", len(list))
	}
	if list[0].SampleRawJSON != `{"event_type":2,"object_action":0}` {
		t.Errorf("expected SampleRawJSON to hold the most recent sample, got %q", list[0].SampleRawJSON)
	}
}
