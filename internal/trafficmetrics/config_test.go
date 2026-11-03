package trafficmetrics

import "testing"

func TestValidateConfigRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"zero alpha", Config{Alpha: 0, Beta: 2.2, LOSLevels: defaultLOSLevels()}},
		{"negative beta", Config{Alpha: 0.5, Beta: -1, LOSLevels: defaultLOSLevels()}},
		{"negative fusion weight", Config{Alpha: 0.5, Beta: 2.2, FusionWeightTTI: -0.1, LOSLevels: defaultLOSLevels()}},
		{"no LOS levels", Config{Alpha: 0.5, Beta: 2.2}},
		{"bad color", Config{Alpha: 0.5, Beta: 2.2, LOSLevels: []LOSLevel{{Label: "A", ZMinPercent: 0, Color: "red"}}}},
		{"empty label", Config{Alpha: 0.5, Beta: 2.2, LOSLevels: []LOSLevel{{Label: "", ZMinPercent: 0, Color: "#000000"}}}},
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
		Alpha: 0.5, Beta: 2.2,
		LOSLevels: []LOSLevel{
			{Label: "F", ZMinPercent: 105, Color: "#D50000"},
			{Label: "A", ZMinPercent: 0, Color: "#00E676"},
		},
	}
	if err := validateConfig(&cfg); err != nil {
		t.Fatalf("validateConfig: %v", err)
	}
	if cfg.LOSLevels[0].Label != "A" || cfg.LOSLevels[1].Label != "F" {
		t.Errorf("LOSLevels not sorted after validateConfig: %+v", cfg.LOSLevels)
	}
}
