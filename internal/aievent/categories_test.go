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
	if _, err := CreateCategoryRule(12, &sub, "Xe sự cố", "xesc"); err != nil {
		t.Fatal(err)
	}
	label, icon, matched := ResolveIcon(12, 41)
	if !matched || label != "Xe sự cố" || icon != "xesc" {
		t.Errorf("got label=%q icon=%q matched=%v", label, icon, matched)
	}
}

func TestResolveIconWildcardSubtype(t *testing.T) {
	setupCategoryTest(t)
	if _, err := CreateCategoryRule(12, nil, "Sự cố chung", "dong"); err != nil {
		t.Fatal(err)
	}
	label, icon, matched := ResolveIcon(12, 999) // subtype never explicitly registered
	if !matched || label != "Sự cố chung" || icon != "dong" {
		t.Errorf("got label=%q icon=%q matched=%v", label, icon, matched)
	}
}

func TestResolveIconExactBeatsWildcard(t *testing.T) {
	setupCategoryTest(t)
	if _, err := CreateCategoryRule(12, nil, "Chung", "dong"); err != nil {
		t.Fatal(err)
	}
	sub := 41
	if _, err := CreateCategoryRule(12, &sub, "Cụ thể", "xesc"); err != nil {
		t.Fatal(err)
	}

	label, icon, matched := ResolveIcon(12, 41)
	if !matched || label != "Cụ thể" || icon != "xesc" {
		t.Errorf("exact rule should win over wildcard, got label=%q icon=%q matched=%v", label, icon, matched)
	}

	// A different subtype of the same typeId falls back to the wildcard rule.
	label2, icon2, matched2 := ResolveIcon(12, 7)
	if !matched2 || label2 != "Chung" || icon2 != "dong" {
		t.Errorf("expected wildcard match for an unlisted subtype, got label=%q icon=%q matched=%v", label2, icon2, matched2)
	}
}

func TestResolveIconNoMatch(t *testing.T) {
	setupCategoryTest(t)
	_, _, matched := ResolveIcon(999, 999)
	if matched {
		t.Error("expected no match for an unconfigured category")
	}
}

func TestCreateCategoryRuleRejectsUnknownIcon(t *testing.T) {
	setupCategoryTest(t)
	if _, err := CreateCategoryRule(1, nil, "Test", "not-a-real-icon"); err == nil {
		t.Error("expected an error for an unknown icon key")
	}
}

func TestCreateCategoryRuleRejectsEmptyLabel(t *testing.T) {
	setupCategoryTest(t)
	if _, err := CreateCategoryRule(1, nil, "", "dong"); err == nil {
		t.Error("expected an error for an empty label")
	}
}

func TestDeleteCategoryRuleRemovesMatch(t *testing.T) {
	setupCategoryTest(t)
	rule, err := CreateCategoryRule(1, nil, "Test", "dong")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteCategoryRule(rule.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, matched := ResolveIcon(1, 1); matched {
		t.Error("expected no match after the rule was deleted")
	}
}

func TestObserveCategoryTracksCountAndLatestSample(t *testing.T) {
	setupCategoryTest(t)
	ObserveCategory(12, 41, "VehicleObstruction", "first desc")
	ObserveCategory(12, 41, "VehicleObstruction", "second desc")
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
	ObserveCategory(12, 41, "A", "")
	ObserveCategory(12, 42, "B", "")
	list := ListObservedCategories()
	if len(list) != 2 {
		t.Errorf("expected 2 distinct observed categories (different subtypeId), got %d", len(list))
	}
}
