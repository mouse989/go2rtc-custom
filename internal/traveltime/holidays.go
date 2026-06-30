package traveltime

import "time"

// hkKind classifies a calendar day for traffic-profile adjustment (Vietnam / HCMC).
type hkKind uint8

const (
	hkNone        hkKind = iota
	hkTet                // Tết Nguyên Đán window (massive traffic drop)
	hkNational           // Fixed national holidays (moderate drop)
	hkSchoolBreak        // School summer break Jun–Aug (morning traffic lighter)
	hkPreHoliday         // Eve of major holiday (afternoon/evening spike)
)

// HolidayKind returns the traffic-relevant holiday classification for a date
// in the Asia/Ho_Chi_Minh timezone and a human-readable Vietnamese label.
func HolidayKind(d time.Time) (hkKind, string) {
	m, day := int(d.Month()), d.Day()

	// ── Fixed national holidays ──────────────────────────────────────────────
	switch {
	case m == 1 && day == 1:
		return hkNational, "Tết Dương lịch (1/1)"
	case m == 4 && day == 18:
		// Giỗ Tổ Hùng Vương: 10/3 âm lịch ≈ Apr 18 (varies ±3 days)
		return hkNational, "Giỗ Tổ Hùng Vương (10/3 âm)"
	case m == 4 && day == 30:
		return hkNational, "Ngày Thống nhất (30/4)"
	case m == 5 && day == 1:
		return hkNational, "Quốc tế Lao động (1/5)"
	case m == 9 && day == 2:
		return hkNational, "Quốc khánh (2/9)"
	}

	// ── Tết Nguyên Đán window ────────────────────────────────────────────────
	if isTetWindow(d) {
		return hkTet, "Tết Nguyên Đán"
	}

	// ── School summer break: June 1 – Aug 31 ────────────────────────────────
	if m >= 6 && m <= 8 {
		return hkSchoolBreak, "Nghỉ hè"
	}

	// ── Pre-holiday eve ──────────────────────────────────────────────────────
	tom := d.AddDate(0, 0, 1)
	tm, tday := int(tom.Month()), tom.Day()
	switch {
	case tm == 1 && tday == 1:
		return hkPreHoliday, "Trước Tết Dương lịch"
	case tm == 4 && tday == 30:
		return hkPreHoliday, "Chiều trước 30/4"
	case tm == 5 && tday == 1:
		return hkPreHoliday, "Chiều trước 1/5"
	case tm == 9 && tday == 2:
		return hkPreHoliday, "Chiều trước Quốc khánh"
	}
	if isTetWindow(tom) && !isTetWindow(d) {
		return hkPreHoliday, "Chiều trước Tết"
	}

	return hkNone, ""
}

// IsHolidayDate returns true if d is a major holiday day (Tết or national).
// Used to exclude outlier days from the "typical weekday" baseline profile.
func IsHolidayDate(d time.Time) bool {
	k, _ := HolidayKind(d)
	return k == hkTet || k == hkNational
}

// HolidayProfileScale returns the TTI profile multiplier for a given holiday
// kind and 15-min slot index (0–95). Returns 1.0 when no adjustment needed.
//
// Scale < 1.0: traffic lighter than a typical weekday (holidays, school break).
// Scale > 1.0: traffic heavier than typical (pre-holiday afternoon spike).
func HolidayProfileScale(kind hkKind, slot int) float64 {
	hour := (slot * fcSlotMin) / 60
	switch kind {
	case hkTet:
		// Tết: residents travel out of HCMC; streets dramatically quieter.
		switch {
		case hour >= 7 && hour <= 9:
			return 0.45 // morning rush nearly absent
		case hour >= 16 && hour <= 19:
			return 0.55 // evening rush also very light
		default:
			return 0.60
		}
	case hkNational:
		// National holidays: moderate traffic reduction (~25–30%).
		switch {
		case hour >= 7 && hour <= 9:
			return 0.70
		case hour >= 16 && hour <= 19:
			return 0.75
		default:
			return 0.82
		}
	case hkSchoolBreak:
		// School break: mainly the school-run commute windows are lighter.
		switch {
		case hour >= 6 && hour <= 8:
			return 0.80 // no school drop-off
		case hour >= 15 && hour <= 16:
			return 0.88 // no school pick-up
		default:
			return 0.95 // rest of day nearly unchanged
		}
	case hkPreHoliday:
		// Pre-holiday eve: afternoon–evening congestion spike (people leave early,
		// out-of-town buses, families heading home).
		switch {
		case hour >= 14 && hour <= 19:
			return 1.28 // heavy afternoon/evening rush
		case hour >= 20 && hour <= 21:
			return 1.12 // still elevated late evening
		default:
			return 1.0
		}
	}
	return 1.0
}

// isTetWindow returns true if d falls within the Tết holiday window.
// Windows: roughly Tết eve (-2) through the 7th day of Tết (+7 from New Year).
// Hardcoded for 2024–2028; uses a broad fallback for other years.
func isTetWindow(d time.Time) bool {
	type win struct{ m1, d1, m2, d2 int }
	tetDates := map[int]win{
		// [year]: {start_month, start_day, end_month, end_day}
		2024: {2, 8, 2, 17},  // Feb 8 – Feb 17 (Tết: Feb 10)
		2025: {1, 27, 2, 5},  // Jan 27 – Feb 5  (Tết: Jan 29)
		2026: {2, 15, 2, 24}, // Feb 15 – Feb 24 (Tết: Feb 17)
		2027: {2, 4, 2, 13},  // Feb 4  – Feb 13 (Tết: Feb 6)
		2028: {1, 24, 2, 2},  // Jan 24 – Feb 2  (Tết: Jan 26)
	}
	m, day := int(d.Month()), d.Day()
	w, ok := tetDates[d.Year()]
	if !ok {
		// Fallback: broad range covering most years
		return (m == 1 && day >= 20) || (m == 2 && day <= 25)
	}
	cur := m*100 + day
	return cur >= w.m1*100+w.d1 && cur <= w.m2*100+w.d2
}
