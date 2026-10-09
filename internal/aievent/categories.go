package aievent

// categories.go — two small, closely-related stores:
//
//   - ObservedCategory: auto-logged, one entry per distinct (typeId,
//     subtypeId) the push endpoint has ever seen, with a sample
//     description/recordType and a running count/first-seen/last-seen. This
//     needs zero admin setup — it exists purely so an admin can SEE what
//     the source system is actually sending before deciding how to map it,
//     instead of configuring rules blind against undocumented codes.
//   - CategoryRule: admin-authored, maps a (typeId, subtypeId-or-wildcard)
//     to a display label + one of the fixed icon keys the Map page already
//     has artwork for (see www/map.html's teMarkerContentFor — "dong",
//     "vachaam", "ngap", "xesc"; anything unmapped/unmatched falls back to
//     a generic marker, same as an unrecognized incidents.html Category
//     already does there).
//
// An event whose category matches no rule is still stored and still shown
// on the map (with the fallback icon) — never dropped. Rules only affect
// how it's drawn, never whether it's ingested.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ValidIconKeys are the only icon keys a CategoryRule may reference —
// closed to exactly the artwork www/map.html ships (traffic-jam.png,
// va-cham.png, ngap-nuoc.png, xe-su-co.png) so a typo in an admin-entered
// icon key can never silently reference nonexistent artwork; "" means "no
// specific icon" (the generic fallback marker).
var ValidIconKeys = map[string]bool{
	"":        true,
	"dong":    true, // traffic-jam.png
	"vachaam": true, // va-cham.png
	"ngap":    true, // ngap-nuoc.png
	"xesc":    true, // xe-su-co.png
}

// CategoryRule maps a source category to a display label + icon.
// SubtypeID == nil means "match any subtype of TypeID" (checked only after
// an exact TypeID+SubtypeID rule fails to match — see ResolveIcon).
type CategoryRule struct {
	ID        string `json:"id"`
	TypeID    int    `json:"type_id"`
	SubtypeID *int   `json:"subtype_id,omitempty"`
	Label     string `json:"label"`
	Icon      string `json:"icon"`
}

// ObservedCategory is one distinct (typeId, subtypeId) combination seen in
// incoming pushes, auto-maintained by ObserveCategory.
type ObservedCategory struct {
	TypeID      int       `json:"type_id"`
	SubtypeID   int       `json:"subtype_id"`
	RecordType  string    `json:"record_type,omitempty"`
	Description string    `json:"description,omitempty"` // most recently received sample
	Count       int       `json:"count"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
}

type categoryStore struct {
	mu       sync.Mutex
	rulesF   string
	obsF     string
	rules    map[string]*CategoryRule     // by ID
	observed map[string]*ObservedCategory // by "typeId:subtypeId"
}

var cstore *categoryStore

func initCategories(dir string) error {
	cstore = &categoryStore{
		rulesF:   filepath.Join(dir, "category_rules.json"),
		obsF:     filepath.Join(dir, "observed_categories.json"),
		rules:    map[string]*CategoryRule{},
		observed: map[string]*ObservedCategory{},
	}
	if err := cstore.loadRules(); err != nil {
		return err
	}
	return cstore.loadObserved()
}

func (s *categoryStore) loadRules() error {
	data, err := os.ReadFile(s.rulesF)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []*CategoryRule
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	for _, r := range list {
		s.rules[r.ID] = r
	}
	return nil
}

func (s *categoryStore) saveRulesLocked() error {
	list := make([]*CategoryRule, 0, len(s.rules))
	for _, r := range s.rules {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Label < list[j].Label })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.rulesF, data, 0644)
}

func (s *categoryStore) loadObserved() error {
	data, err := os.ReadFile(s.obsF)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []*ObservedCategory
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	for _, o := range list {
		s.observed[observedKey(o.TypeID, o.SubtypeID)] = o
	}
	return nil
}

func (s *categoryStore) saveObservedLocked() error {
	list := make([]*ObservedCategory, 0, len(s.observed))
	for _, o := range s.observed {
		list = append(list, o)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].LastSeen.After(list[j].LastSeen) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.obsF, data, 0644)
}

func observedKey(typeID, subtypeID int) string { return fmt.Sprintf("%d:%d", typeID, subtypeID) }

// ObserveCategory records one sighting of (typeID, subtypeID) — called on
// every push, regardless of whether a CategoryRule already matches it, so
// the "what has OMNIA actually sent" view stays complete even after rules
// are configured.
func ObserveCategory(typeID, subtypeID int, recordType, description string) {
	now := time.Now()
	cstore.mu.Lock()
	defer cstore.mu.Unlock()
	key := observedKey(typeID, subtypeID)
	o, ok := cstore.observed[key]
	if !ok {
		o = &ObservedCategory{TypeID: typeID, SubtypeID: subtypeID, FirstSeen: now}
		cstore.observed[key] = o
	}
	o.Count++
	o.LastSeen = now
	if recordType != "" {
		o.RecordType = recordType
	}
	if description != "" {
		o.Description = description
	}
	_ = cstore.saveObservedLocked() // best-effort — never blocks ingestion on a write failure
}

// ListObservedCategories returns every distinct category seen, most
// recently seen first.
func ListObservedCategories() []ObservedCategory {
	cstore.mu.Lock()
	defer cstore.mu.Unlock()
	out := make([]ObservedCategory, 0, len(cstore.observed))
	for _, o := range cstore.observed {
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// ListCategoryRules returns every configured rule, by label.
func ListCategoryRules() []CategoryRule {
	cstore.mu.Lock()
	defer cstore.mu.Unlock()
	out := make([]CategoryRule, 0, len(cstore.rules))
	for _, r := range cstore.rules {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// CreateCategoryRule adds a new mapping rule.
func CreateCategoryRule(typeID int, subtypeID *int, label, icon string) (*CategoryRule, error) {
	if label == "" {
		return nil, errAIEvent("label required")
	}
	if !ValidIconKeys[icon] {
		return nil, errAIEvent("unknown icon key")
	}
	r := &CategoryRule{ID: randSuffix() + randSuffix(), TypeID: typeID, SubtypeID: subtypeID, Label: label, Icon: icon}
	cstore.mu.Lock()
	defer cstore.mu.Unlock()
	cstore.rules[r.ID] = r
	if err := cstore.saveRulesLocked(); err != nil {
		delete(cstore.rules, r.ID)
		return nil, err
	}
	cp := *r
	return &cp, nil
}

// UpdateCategoryRule replaces an existing rule's fields.
func UpdateCategoryRule(id string, typeID int, subtypeID *int, label, icon string) (*CategoryRule, error) {
	if label == "" {
		return nil, errAIEvent("label required")
	}
	if !ValidIconKeys[icon] {
		return nil, errAIEvent("unknown icon key")
	}
	cstore.mu.Lock()
	defer cstore.mu.Unlock()
	r, ok := cstore.rules[id]
	if !ok {
		return nil, errAIEventNotFound
	}
	r.TypeID, r.SubtypeID, r.Label, r.Icon = typeID, subtypeID, label, icon
	if err := cstore.saveRulesLocked(); err != nil {
		return nil, err
	}
	cp := *r
	return &cp, nil
}

// DeleteCategoryRule removes a rule — matching events fall back to the
// generic marker again, nothing about stored events changes.
func DeleteCategoryRule(id string) error {
	cstore.mu.Lock()
	defer cstore.mu.Unlock()
	if _, ok := cstore.rules[id]; !ok {
		return errAIEventNotFound
	}
	delete(cstore.rules, id)
	return cstore.saveRulesLocked()
}

// ResolveIcon finds the best-matching rule for (typeID, subtypeID): an
// exact TypeID+SubtypeID rule first, then a TypeID-only wildcard rule
// (SubtypeID == nil), else matched=false (caller falls back to a generic
// marker — see www/map.html's teMarkerContentFor-style default).
func ResolveIcon(typeID, subtypeID int) (label, icon string, matched bool) {
	cstore.mu.Lock()
	defer cstore.mu.Unlock()
	var wildcard *CategoryRule
	for _, r := range cstore.rules {
		if r.TypeID != typeID {
			continue
		}
		if r.SubtypeID == nil {
			wildcard = r
			continue
		}
		if *r.SubtypeID == subtypeID {
			return r.Label, r.Icon, true
		}
	}
	if wildcard != nil {
		return wildcard.Label, wildcard.Icon, true
	}
	return "", "", false
}

type aiEventError string

func (e aiEventError) Error() string { return string(e) }
func errAIEvent(msg string) error    { return aiEventError(msg) }

var errAIEventNotFound = aiEventError("not found")
