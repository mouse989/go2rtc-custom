package trafficmetrics

import "testing"

func TestValidateConfigRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"negative fusion weight", Config{FusionWeightTTI: -0.1, LOSLevels: defaultLOSLevels()}},
		{"negative bottleneck threshold", Config{BottleneckJFSpread: -1, LOSLevels: defaultLOSLevels()}},
		{"negative anomaly threshold", Config{AnomalyJFDelta: -1, LOSLevels: defaultLOSLevels()}},
		{"no LOS levels", Config{}},
		{"bad color", Config{LOSLevels: []LOSLevel{{Label: "A", TTIMin: 1.0, Color: "red"}}}},
		{"empty label", Config{LOSLevels: []LOSLevel{{Label: "", TTIMin: 1.0, Color: "#000000"}}}},
		{"negative ttiMin", Config{LOSLevels: []LOSLevel{{Label: "A", TTIMin: -1, Color: "#000000"}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := validateConfig(&c.cfg); err == nil {
				t.Errorf("validateConfig(%s) = nil, want an error", c.name)
			}
		})
	}
}

func TestValidateConfigAcceptsDefault(t *testing.T) {
	cfg := defaultConfig()
	if err := validateConfig(&cfg); err != nil {
		t.Errorf("validateConfig(default) = %v, want nil", err)
	}
}

func TestValidateConfigSortsLevels(t *testing.T) {
	cfg := Config{
		LOSLevels: []LOSLevel{
			{Label: "F", TTIMin: 3.45, Color: "#D50000"},
			{Label: "A", TTIMin: 1.0, Color: "#00E676"},
		},
	}
	if err := validateConfig(&cfg); err != nil {
		t.Fatalf("validateConfig: %v", err)
	}
	if cfg.LOSLevels[0].Label != "A" || cfg.LOSLevels[1].Label != "F" {
		t.Errorf("LOSLevels not sorted after validateConfig: %+v", cfg.LOSLevels)
	}
}
