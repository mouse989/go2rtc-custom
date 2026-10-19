package trafficflow

// pushlog.go — a short rolling admin-only log of every attempt to call
// POST /api/trafficdata/hawkwise/v1/flow-push, SQLite-backed like the rest
// of this package. Exists purely so an admin can see what the external
// system is actually sending: once records.go parses a push into a
// FlowRecord, the original wire JSON is gone, and a request that never got
// that far (missing/invalid API key) leaves no trace anywhere else at all —
// there was previously no way to answer "what is the other side actually
// sending us" short of a packet capture.
//
// A request rejected before its body is read (missing/invalid key, IP/
// integration rate limit, wrong method/content-type) is still logged — so
// "is anything even hitting this endpoint" is answerable without first
// creating an integration key — but its body is never read off the wire
// for that entry, preserving handlePush's existing fast-reject-without-
// reading-the-body behavior for unauthenticated callers. Reading up to
// maxPushBodyBytes from every unauthenticated request before validating
// its key would be a meaningfully bigger DoS surface on a path that's
// public by design (internal/auth's isPublicPath) — this log never
// expands that surface, it only records what already happened.
//
// Capped at pushLogMaxRows, pruned on every insert: this is a short-term
// debugging aid, not a durable audit log. At up to pushRateLimitPerMinute
// requests/min per integration (api.go) and potentially many cameras
// pushing through it, an unbounded table would grow unmanageably fast —
// 3000 rows × the ~32KB body cap below is a worst case of under 100MB,
// which is fine for a local SQLite file.
//
// ListPushLog groups nothing itself — same "fetch a bounded list, group
// client-side" approach the admin UI already uses elsewhere (e.g.
// dashboard2.html's incident analytics) — it just filters by time range
// and resolves each entry's source_uuid against the sensors table (a plain
// LEFT JOIN, done live on every query) so a mapping created AFTER some
// pushes already arrived still shows up against their past entries too,
// not just future ones.

import (
	"database/sql"
	"time"
)

const pushLogMaxRows = 3000

// pushLogBodyMaxChars bounds how much of one request body gets stored —
// generous for any real Hawkwise payload (even with every lane/vehicle-
// class field populated it's well under this), just a backstop against a
// deliberately oversized authenticated push bloating the log.
const pushLogBodyMaxChars = 32 * 1024

// PushLogEntry is one logged attempt to call the push endpoint.
type PushLogEntry struct {
	ID            int64  `json:"id"`
	ReceivedAt    string `json:"received_at"` // RFC3339Nano, UTC
	RemoteIP      string `json:"remote_ip"`
	AuthResult    string `json:"auth_result"` // ok | missing_api_key | invalid_api_key | rate_limited_ip | rate_limited_integration | bad_method | bad_content_type
	Integration   string `json:"integration,omitempty"` // matched integration's name — only set when auth_result=ok
	SourceUUID    string `json:"source_uuid,omitempty"` // general.camera.source_uuid — only set once the body parsed far enough to read it
	SensorLabel   string `json:"sensor_label,omitempty"` // this source_uuid's current Sensor mapping label, if any — resolved at query time, not stored
	HTTPStatus    int    `json:"http_status"`
	BodyValidJSON *bool  `json:"body_valid_json,omitempty"` // nil when auth failed before the body was ever read
	ParseError    string `json:"parse_error,omitempty"`
	Body          string `json:"body,omitempty"`
	BodyTruncated bool   `json:"body_truncated,omitempty"`
}

// PushLogFilter bounds a ListPushLog query. A zero Time means "no bound on
// that side" — the UI is expected to always send both (see api.go's
// handlePushLog), but the zero value stays safe for callers that don't.
type PushLogFilter struct {
	From time.Time
	To   time.Time
}

func nullBoolInt(b *bool) any {
	if b == nil {
		return nil
	}
	return boolInt(*b)
}

func boolPtrFromNullInt(n sql.NullInt64) *bool {
	if !n.Valid {
		return nil
	}
	v := n.Int64 != 0
	return &v
}

// logPushAttempt records one request to the push endpoint. Called from
// handlePush at every exit point (success and every rejection reason) —
// errors are only logged, never surfaced to the caller, since this is a
// debugging aid and must never affect whether the push itself succeeds.
func logPushAttempt(e PushLogEntry) {
	if e.ReceivedAt == "" {
		e.ReceivedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	body := e.Body
	truncated := e.BodyTruncated
	if len(body) > pushLogBodyMaxChars {
		body = body[:pushLogBodyMaxChars]
		truncated = true
	}
	_, err := db.Exec(`
		INSERT INTO push_log (received_at, remote_ip, auth_result, integration, http_status, body_valid_json, parse_error, body, body_truncated, source_uuid)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, e.ReceivedAt, e.RemoteIP, e.AuthResult, e.Integration, e.HTTPStatus, nullBoolInt(e.BodyValidJSON), e.ParseError, body, boolInt(truncated), e.SourceUUID)
	if err != nil {
		log.Warn().Err(err).Msg("[trafficflow] logPushAttempt insert failed (continuing)")
		return
	}
	if _, err := db.Exec(`DELETE FROM push_log WHERE id NOT IN (SELECT id FROM push_log ORDER BY id DESC LIMIT ?)`, pushLogMaxRows); err != nil {
		log.Warn().Err(err).Msg("[trafficflow] push_log prune failed (continuing)")
	}
}

// ListPushLog returns logged push attempts within filter's time range
// (either side left zero means unbounded on that side), newest first, each
// resolved against the CURRENT sensors mapping for its source_uuid.
func ListPushLog(filter PushLogFilter) []PushLogEntry {
	where := ""
	args := []any{}
	if !filter.From.IsZero() {
		where += " AND p.received_at >= ?"
		args = append(args, filter.From.UTC().Format(time.RFC3339Nano))
	}
	if !filter.To.IsZero() {
		where += " AND p.received_at <= ?"
		args = append(args, filter.To.UTC().Format(time.RFC3339Nano))
	}
	rows, err := db.Query(`
		SELECT p.id, p.received_at, p.remote_ip, p.auth_result, p.integration, p.http_status,
		       p.body_valid_json, p.parse_error, p.body, p.body_truncated, p.source_uuid,
		       COALESCE(s.label, '')
		FROM push_log p
		LEFT JOIN sensors s ON s.source_uuid = p.source_uuid AND p.source_uuid != ''
		WHERE 1=1`+where+`
		ORDER BY p.id DESC
	`, args...)
	if err != nil {
		log.Warn().Err(err).Msg("[trafficflow] ListPushLog query failed")
		return nil
	}
	defer rows.Close()

	out := make([]PushLogEntry, 0, 256)
	for rows.Next() {
		var e PushLogEntry
		var validJSON sql.NullInt64
		var truncated int
		if err := rows.Scan(&e.ID, &e.ReceivedAt, &e.RemoteIP, &e.AuthResult, &e.Integration, &e.HTTPStatus,
			&validJSON, &e.ParseError, &e.Body, &truncated, &e.SourceUUID, &e.SensorLabel); err != nil {
			continue
		}
		e.BodyValidJSON = boolPtrFromNullInt(validJSON)
		e.BodyTruncated = truncated != 0
		out = append(out, e)
	}
	return out
}

// ClearPushLog deletes every retained entry — handy for an admin to reset
// the log right before a test push, so the next entry is easy to find.
func ClearPushLog() error {
	_, err := db.Exec(`DELETE FROM push_log`)
	return err
}
