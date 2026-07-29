package incidents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TimeWindow is one named peak-hour window, e.g. "Cao điểm sáng" 06:00–08:30.
// Start/End are "HH:MM", always interpreted in Vietnam wall-clock time
// (Asia/Ho_Chi_Minh) regardless of what timezone the server process itself
// runs in or what zone a given Incident.Time carries.
type TimeWindow struct {
	Name  string `json:"name"`
	Start string `json:"start"` // "HH:MM"
	End   string `json:"end"`   // "HH:MM", exclusive; End < Start wraps past midnight
}

type timeSlotConfig struct {
	Windows      []TimeWindow `json:"windows"`
	DefaultLabel string       `json:"default_label"` // catch-all for anything outside every window
}

var defaultWindows = []TimeWindow{
	{Name: "Cao điểm sáng", Start: "06:00", End: "08:30"},
	{Name: "Cao điểm chiều", Start: "16:30", End: "19:00"},
}

const defaultOffPeakLabel = "Ngoài giờ cao điểm"

var (
	slotMu   sync.RWMutex
	slotCfg  timeSlotConfig
	slotFile string

	// vnLocation anchors "what hour is it" to Vietnam time no matter where
	// go2rtc happens to be deployed/run — without this, a time.Time that
	// round-tripped through JSON as UTC (e.g. "15:05 VN" stored as "08:05Z")
	// silently misclassifies using the UTC hour instead of the VN one.
	vnLocation = loadVNLocation()
)

func loadVNLocation() *time.Location {
	if loc, err := time.LoadLocation("Asia/Ho_Chi_Minh"); err == nil {
		return loc
	}
	// Fallback for minimal containers without IANA tzdata installed.
	return time.FixedZone("ICT", 7*3600)
}

func initTimeSlots() {
	slotFile = filepath.Join(dataDir, "time_slots.json")
	slotCfg = timeSlotConfig{Windows: defaultWindows, DefaultLabel: defaultOffPeakLabel}

	data, err := os.ReadFile(slotFile)
	if err != nil {
		return // fine on first run — defaults stand
	}
	var c timeSlotConfig
	if json.Unmarshal(data, &c) == nil && len(c.Windows) > 0 {
		if c.DefaultLabel == "" {
			c.DefaultLabel = defaultOffPeakLabel
		}
		slotCfg = c
	}
}

func saveTimeSlotsLocked() error {
	data, err := json.MarshalIndent(slotCfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(slotFile, data, 0644)
}

// GetTimeWindows returns the current configured windows and off-peak label.
func GetTimeWindows() ([]TimeWindow, string) {
	slotMu.RLock()
	defer slotMu.RUnlock()
	out := make([]TimeWindow, len(slotCfg.Windows))
	copy(out, slotCfg.Windows)
	return out, slotCfg.DefaultLabel
}

// SetTimeWindows validates and persists a new set of windows, replacing the
// previous configuration entirely.
func SetTimeWindows(windows []TimeWindow, defaultLabel string) error {
	cleaned := make([]TimeWindow, 0, len(windows))
	for i, w := range windows {
		name := strings.TrimSpace(w.Name)
		if name == "" {
			return fmt.Errorf("khung giờ #%d: tên không được để trống", i+1)
		}
		if _, err := parseHHMM(w.Start); err != nil {
			return fmt.Errorf("khung giờ %q: giờ bắt đầu không hợp lệ (%q)", name, w.Start)
		}
		if _, err := parseHHMM(w.End); err != nil {
			return fmt.Errorf("khung giờ %q: giờ kết thúc không hợp lệ (%q)", name, w.End)
		}
		cleaned = append(cleaned, TimeWindow{Name: name, Start: w.Start, End: w.End})
	}
	defaultLabel = strings.TrimSpace(defaultLabel)
	if defaultLabel == "" {
		defaultLabel = defaultOffPeakLabel
	}

	slotMu.Lock()
	slotCfg = timeSlotConfig{Windows: cleaned, DefaultLabel: defaultLabel}
	err := saveTimeSlotsLocked()
	slotMu.Unlock()
	return err
}

func parseHHMM(s string) (int, error) {
	h, m, ok := strings.Cut(s, ":")
	if !ok {
		return 0, fmt.Errorf("bad format")
	}
	hh, err := strconv.Atoi(h)
	if err != nil || hh < 0 || hh > 23 {
		return 0, fmt.Errorf("bad hour")
	}
	mm, err := strconv.Atoi(m)
	if err != nil || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("bad minute")
	}
	return hh*60 + mm, nil
}

// ComputeTimeSlot derives Khung giờ from a timestamp, classified by the
// admin-configured windows against Vietnam wall-clock time-of-day.
func ComputeTimeSlot(t time.Time) string {
	vn := t.In(vnLocation)
	m := vn.Hour()*60 + vn.Minute()

	slotMu.RLock()
	defer slotMu.RUnlock()
	for _, w := range slotCfg.Windows {
		start, err1 := parseHHMM(w.Start)
		end, err2 := parseHHMM(w.End)
		if err1 != nil || err2 != nil {
			continue
		}
		if start <= end {
			if m >= start && m < end {
				return w.Name
			}
		} else if m >= start || m < end { // wraps past midnight
			return w.Name
		}
	}
	return slotCfg.DefaultLabel
}
