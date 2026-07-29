// Package incidents tracks manually-logged traffic incident events (đông xe,
// va chạm, sự cố khác) reported by CSGT units — the same data staff
// previously kept in a shared Google Sheet / Excel file. Storage is one JSON
// file per year (mirrors the "one sheet per year" convention of the source
// spreadsheet), kept small enough to load entirely into memory.
package incidents

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/rs/zerolog"
)

// Incident is one logged event. Field names/order intentionally mirror the
// legacy Excel columns (Thời gian, Thể loại, Nội dung, Đơn vị, Ghi chú, Vị
// trí) so staff moving from the spreadsheet to this form see the same shape.
type Incident struct {
	ID        string    `json:"id"`
	Time      time.Time `json:"time"`           // Thời gian
	Category  string    `json:"category"`       // Thể loại
	Content   string    `json:"content"`        // Nội dung sự việc, tình trạng xử lý
	Unit      string    `json:"unit"`           // Đơn vị
	Note      string    `json:"note,omitempty"` // Ghi chú
	Location  string    `json:"location"`       // Vị trí
	TimeSlot  string    `json:"time_slot"`      // Khung giờ — auto-computed from Time
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedBy string    `json:"updated_by,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// Time-slot windows (Khung giờ) are admin-configurable — see timeslots.go for
// ComputeTimeSlot, GetTimeWindows, SetTimeWindows.

var log zerolog.Logger

var (
	mu       sync.RWMutex
	years    = map[int]map[string]*Incident{} // year -> id -> incident
	dataDir  string
	metaData meta
)

// meta holds the known-value lists that back the form's dropdowns (Thể
// loại / Đơn vị) and the location autocomplete, so manual entry and Excel
// import always normalize to the same vocabulary.
type meta struct {
	Categories []string `json:"categories"`
	Units      []string `json:"units"`
	Locations  []string `json:"locations"` // most-used locations, capped
}

var defaultCategories = []string{"Đông xe", "Va chạm", "Sự cố khác"}

var defaultUnits = []string{
	"ĐỘI CSGT TÂN SƠN NHẤT", "ĐỘI CSGT BẾN THÀNH", "ĐỘI CSGT CÁT LÁI",
	"ĐỘI CSGT RẠCH CHIẾC", "ĐỘI CSGT HÀNG XANH", "ĐỘI CSGT AN SƯƠNG",
	"TRẠM CSGT ĐA PHƯỚC", "TRẠM CSGT TÂN TÚC", "ĐỘI TUẦN TRA, DẪN ĐOÀN",
	"ĐỘI CSGT PHÚ LÂM", "ĐỘI CSGT CHỢ LỚN", "ĐỘI CSGT AN LẠC", "PC08",
	"ĐỘI CSGT BÀN CỜ", "ĐỘI CSGT NAM SÀI GÒN", "ĐỘI CSGT NAM SÀI GÒN 2",
	"TRẠM CSGT TÂY BẮC", "ĐỘI CSGT BÌNH TRIỆU",
}

// Init loads (or initializes) the incidents store. Call once at startup.
func Init() {
	log = app.GetLogger("incidents")

	dataDir = "incidents_data"
	if app.ConfigPath != "" {
		dataDir = filepath.Join(filepath.Dir(app.ConfigPath), "incidents_data")
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Error().Err(err).Msg("[incidents] cannot create data dir")
	}

	loadMeta()
	initTimeSlots()

	// Eagerly load the current year so first request is fast.
	_ = loadYear(time.Now().Year())

	registerHandlers()

	log.Info().Str("dir", dataDir).Msg("[incidents] ready")
}

func yearFile(year int) string {
	return filepath.Join(dataDir, "incidents_"+strconv.Itoa(year)+".json")
}

func metaFile() string {
	return filepath.Join(dataDir, "meta.json")
}

func loadMeta() {
	metaData = meta{Categories: defaultCategories, Units: defaultUnits}
	data, err := os.ReadFile(metaFile())
	if err != nil {
		return // fine on first run — defaults stand
	}
	var m meta
	if err := json.Unmarshal(data, &m); err == nil {
		if len(m.Categories) > 0 {
			metaData.Categories = m.Categories
		}
		if len(m.Units) > 0 {
			metaData.Units = m.Units
		}
		metaData.Locations = m.Locations
	}
}

// saveMetaLocked persists metaData. Caller must hold mu (write lock).
func saveMetaLocked() {
	data, err := json.MarshalIndent(metaData, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(metaFile(), data, 0644)
}

// learnLocked adds newly-seen category/unit/location values to the known
// lists (deduped), so future dropdowns include them. Caller must hold mu.
func learnLocked(in *Incident) {
	addUnique(&metaData.Categories, in.Category)
	addUnique(&metaData.Units, in.Unit)
	if in.Location != "" {
		addUnique(&metaData.Locations, in.Location)
		// Cap the location list so it doesn't grow unbounded (907+ distinct
		// road names in the legacy data) — keep the most recently seen.
		const maxLocations = 500
		if len(metaData.Locations) > maxLocations {
			metaData.Locations = metaData.Locations[len(metaData.Locations)-maxLocations:]
		}
	}
}

func addUnique(list *[]string, v string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return
	}
	for _, x := range *list {
		if x == v {
			return
		}
	}
	*list = append(*list, v)
}

// loadYear loads a year's file into memory if not already loaded. Safe to
// call repeatedly. Missing file = empty year (not an error).
func loadYear(year int) error {
	mu.Lock()
	defer mu.Unlock()
	return loadYearLocked(year)
}

func loadYearLocked(year int) error {
	if _, ok := years[year]; ok {
		return nil
	}
	m := map[string]*Incident{}
	data, err := os.ReadFile(yearFile(year))
	if err != nil {
		years[year] = m
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var list []*Incident
	if err := json.Unmarshal(data, &list); err != nil {
		years[year] = m
		return err
	}
	for _, in := range list {
		m[in.ID] = in
	}
	years[year] = m
	return nil
}

// saveYearLocked persists one year's incidents, sorted by time. Caller must
// hold mu (write lock) and have that year already loaded.
func saveYearLocked(year int) error {
	list := make([]*Incident, 0, len(years[year]))
	for _, in := range years[year] {
		list = append(list, in)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Time.Before(list[j].Time) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(yearFile(year), data, 0644)
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ListYears returns the years that have at least one saved incidents file,
// newest first, so the UI can offer a year picker without loading everything.
func ListYears() []int {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "incidents_") || !strings.HasSuffix(name, ".json") {
			continue
		}
		yStr := strings.TrimSuffix(strings.TrimPrefix(name, "incidents_"), ".json")
		if y, err := strconv.Atoi(yStr); err == nil {
			out = append(out, y)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(out)))
	return out
}

// ListFilter narrows List() results.
type ListFilter struct {
	Year     int
	From, To time.Time // zero = unbounded
	Category string
	Unit     string
	Q        string // free-text search in Content/Location/Note
}

// List returns incidents for filter.Year matching the remaining filters,
// newest first.
func List(f ListFilter) ([]*Incident, error) {
	if err := loadYear(f.Year); err != nil {
		return nil, err
	}
	mu.RLock()
	defer mu.RUnlock()

	q := strings.ToLower(strings.TrimSpace(f.Q))
	out := make([]*Incident, 0, len(years[f.Year]))
	for _, in := range years[f.Year] {
		if f.Category != "" && in.Category != f.Category {
			continue
		}
		if f.Unit != "" && in.Unit != f.Unit {
			continue
		}
		if !f.From.IsZero() && in.Time.Before(f.From) {
			continue
		}
		if !f.To.IsZero() && in.Time.After(f.To) {
			continue
		}
		if q != "" &&
			!strings.Contains(strings.ToLower(in.Content), q) &&
			!strings.Contains(strings.ToLower(in.Location), q) &&
			!strings.Contains(strings.ToLower(in.Note), q) {
			continue
		}
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out, nil
}

// Get fetches one incident by year+id.
func Get(year int, id string) (*Incident, bool) {
	if err := loadYear(year); err != nil {
		return nil, false
	}
	mu.RLock()
	defer mu.RUnlock()
	in, ok := years[year][id]
	return in, ok
}

// Create validates and stores a new incident, filling ID/TimeSlot/CreatedAt.
func Create(in *Incident, username string) error {
	if err := validate(in); err != nil {
		return err
	}
	in.ID = randID()
	in.TimeSlot = ComputeTimeSlot(in.Time)
	in.CreatedBy = username
	in.CreatedAt = time.Now()

	year := in.Time.Year()
	mu.Lock()
	defer mu.Unlock()
	if err := loadYearLocked(year); err != nil {
		return err
	}
	years[year][in.ID] = in
	learnLocked(in)
	saveMetaLocked()
	return saveYearLocked(year)
}

// Update overwrites an existing incident (looked up by year+ID). If the
// edited Time moves the event into a different year, it's relocated.
func Update(year int, in *Incident, username string) error {
	if err := validate(in); err != nil {
		return err
	}
	if in.ID == "" {
		return errNotFound
	}

	mu.Lock()
	defer mu.Unlock()

	if err := loadYearLocked(year); err != nil {
		return err
	}
	existing, ok := years[year][in.ID]
	if !ok {
		return errNotFound
	}

	in.TimeSlot = ComputeTimeSlot(in.Time)
	in.CreatedBy = existing.CreatedBy
	in.CreatedAt = existing.CreatedAt
	in.UpdatedBy = username
	in.UpdatedAt = time.Now()

	newYear := in.Time.Year()
	learnLocked(in)
	saveMetaLocked()

	if newYear == year {
		years[year][in.ID] = in
		return saveYearLocked(year)
	}

	// Moved to a different year's file.
	delete(years[year], in.ID)
	if err := loadYearLocked(newYear); err != nil {
		return err
	}
	years[newYear][in.ID] = in
	if err := saveYearLocked(year); err != nil {
		return err
	}
	return saveYearLocked(newYear)
}

// Delete removes an incident by year+id.
func Delete(year int, id string) error {
	mu.Lock()
	defer mu.Unlock()
	if err := loadYearLocked(year); err != nil {
		return err
	}
	if _, ok := years[year][id]; !ok {
		return errNotFound
	}
	delete(years[year], id)
	return saveYearLocked(year)
}

// RecomputeAllTimeSlots reloads every saved year and, for every incident:
// (1) re-derives TimeSlot using the current window config, and (2) re-tags
// Time onto vnLocation so storage stays unified even for records saved
// before that normalization existed (validate() now does this for every new
// write; this is the one-time catch-up for what's already on disk). Saves
// any year that changed. Note: this can only correctly RE-LABEL an already
// correct instant (e.g. records entered via the web form, which always
// captured the right moment in time) — it cannot fix a record whose absolute
// instant was itself wrong (e.g. an Excel import processed while the server
// OS clock was not set to Vietnam time, before parseExcelTime pinned to
// vnLocation explicitly); those need re-entry or re-import.
func RecomputeAllTimeSlots() (int, error) {
	mu.Lock()
	defer mu.Unlock()

	updated := 0
	for _, year := range ListYears() {
		if err := loadYearLocked(year); err != nil {
			return updated, err
		}
		changed := false
		for _, in := range years[year] {
			newTime := in.Time.In(vnLocation)
			newSlot := ComputeTimeSlot(in.Time)
			if newSlot != in.TimeSlot || !newTime.Equal(in.Time) || newTime.Location() != in.Time.Location() {
				in.Time = newTime
				in.TimeSlot = newSlot
				changed = true
				updated++
			}
		}
		if changed {
			if err := saveYearLocked(year); err != nil {
				return updated, err
			}
		}
	}
	return updated, nil
}

// Meta returns the current dropdown/autocomplete value lists.
func Meta() meta {
	mu.RLock()
	defer mu.RUnlock()
	return metaData
}

var errNotFound = notFoundError{}

type notFoundError struct{}

func (notFoundError) Error() string { return "incident not found" }

func validate(in *Incident) error {
	in.Category = strings.TrimSpace(in.Category)
	in.Content = strings.TrimSpace(in.Content)
	in.Unit = strings.TrimSpace(in.Unit)
	in.Location = strings.TrimSpace(in.Location)
	in.Note = strings.TrimSpace(in.Note)

	// Unify storage on Vietnam time: the web form sends UTC (browser
	// `.toISOString()`), Excel import already parses as vnLocation — from
	// here on every stored Incident.Time carries the same +07:00 offset, so
	// anything that reads Time directly (e.g. Excel export, which has no
	// timezone concept of its own) shows the correct wall-clock time without
	// having to remember to convert at every call site.
	if !in.Time.IsZero() {
		in.Time = in.Time.In(vnLocation)
	}

	// Required fields: Thời gian, Thể loại, Nội dung, Đơn vị. Vị trí/Ghi chú
	// are optional — a meaningful share of the legacy data has no location
	// (general/area-wide reports), so requiring it would reject valid rows.
	switch {
	case in.Time.IsZero():
		return validationError("thời gian không được để trống")
	case in.Category == "":
		return validationError("thể loại không được để trống")
	case in.Content == "":
		return validationError("nội dung sự việc không được để trống")
	case in.Unit == "":
		return validationError("đơn vị không được để trống")
	}
	return nil
}

type validationError string

func (e validationError) Error() string { return string(e) }
