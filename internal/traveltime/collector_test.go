package traveltime

import "testing"

func TestParseLatLng(t *testing.T) {
	cases := []struct {
		in      string
		wantLat float64
		wantLng float64
	}{
		{"10.77,106.70", 10.77, 106.70},
		{" 10.77 , 106.70 ", 10.77, 106.70},
		{"10.776530,106.700981", 10.776530, 106.700981},
		{"not-a-point", 0, 0},
		{"", 0, 0},
		{"10.77", 0, 0}, // missing lng
	}
	for _, c := range cases {
		lat, lng := parseLatLng(c.in)
		if lat != c.wantLat || lng != c.wantLng {
			t.Errorf("parseLatLng(%q) = (%v, %v), want (%v, %v)", c.in, lat, lng, c.wantLat, c.wantLng)
		}
	}
}

func TestRoundTTI(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{1.0, 1.0},
		{1.005, 1.0}, // rounds to nearest .01, not up blindly
		{1.236, 1.24},
		{1.234, 1.23},
		{2.0, 2.0},
	}
	for _, c := range cases {
		if got := roundTTI(c.in); got != c.want {
			t.Errorf("roundTTI(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
