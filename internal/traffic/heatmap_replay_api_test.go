package traffic

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// handleHeatmapReplayPoints's own gate is just "is there an authenticated
// user at all" (auth.UserFromContext) — the real restriction, requiring the
// TabHeatmapReplay tab specifically, runs earlier in
// internal/auth/middleware.go's tabExtraPaths before the request ever
// reaches here, and isn't reachable from this package's tests. What belongs
// here is the request's own validation logic. internal/auth doesn't export
// a way to inject an authenticated user into a context.Context from outside
// the package, so the from/to validation branches (which only run after
// that check passes) aren't exercised directly; they mirror
// handlePersistentExport's already-reviewed, equally untested validation,
// and the underlying data path is covered by
// TestPersistentPointsInRangeFiltersByExactTimestamp.
func TestHandleHeatmapReplayPointsRequiresAuth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/heatmap-replay/points?from=2026-10-26T00:00:00Z&to=2026-10-26T23:59:59Z", nil)
	rec := httptest.NewRecorder()

	handleHeatmapReplayPoints(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for request with no authenticated user, got %d: %s", rec.Code, rec.Body.String())
	}
}
