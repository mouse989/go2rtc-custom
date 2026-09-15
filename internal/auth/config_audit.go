package auth

// Camera-config audit tool: an admin managing a camera list separately
// (a spreadsheet, a field survey, whatever) has no way to tell which of
// those cameras never made it into go2rtc — the /admin.html "Config Audit"
// panel calls this endpoint and compares its own list of expected camera
// names against the two lists here to sort every name into:
//
//   - OK              — declared in go2rtc.yaml AND currently live
//   - declared-not-live — in go2rtc.yaml's streams: mapping, but
//     internal/streams never registered it (bad indentation, an invalid
//     producer URL, or a duplicate key silently overwriting it — see
//     Duplicates below)
//   - missing          — not found in go2rtc.yaml's streams: mapping at all
//
// The comparison itself happens client-side (admin.html) — this endpoint
// only ever reports the server's own two lists, so admins aren't forced to
// upload or store their external tracking list on the server.

import (
	"net/http"

	"github.com/AlexxIT/go2rtc/internal/app"
)

// ConfigAuditResult is the payload for GET /api/config-audit.
type ConfigAuditResult struct {
	ParseOK    bool     `json:"parse_ok"` // false if go2rtc.yaml couldn't be read/parsed as YAML
	ParseError string   `json:"parse_error,omitempty"`
	Declared   []string `json:"declared"`   // streams: keys, in file order, duplicates included
	Duplicates []string `json:"duplicates"` // keys declared more than once (silently clobber each other in the live map)
	Live       []string `json:"live"`       // currently active in the running server (internal/streams)
}

func registerConfigAuditHandler() {
	http.HandleFunc("/api/config-audit", configAuditHandler)
}

// configAuditHandler GET /api/config-audit  (admin only)
func configAuditHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	result := ConfigAuditResult{
		Declared:   []string{},
		Duplicates: []string{},
		Live:       []string{},
	}

	if declared, err := readDeclaredStreamNames(app.ConfigPath); err != nil {
		result.ParseError = err.Error()
	} else {
		result.ParseOK = true
		result.Declared = declared
		result.Duplicates = duplicateNames(declared)
	}

	if getStreamNames != nil {
		if live := getStreamNames(); live != nil {
			result.Live = live
		}
	}

	responseJSON(w, result)
}

// duplicateNames returns the names in names that occur more than once,
// order not significant (the frontend only needs the set for a warning).
func duplicateNames(names []string) []string {
	seen := make(map[string]int, len(names))
	for _, n := range names {
		seen[n]++
	}
	var dupes []string
	for n, count := range seen {
		if count > 1 {
			dupes = append(dupes, n)
		}
	}
	return dupes
}
