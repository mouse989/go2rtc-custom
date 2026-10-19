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
// requests/min (api.go), an unbounded table would grow unmanageably fast.

import (
	"database/sql"
	"time"
)

const pushLogMaxRows = 300

// pushLogBodyMaxChars bounds how much of one request body gets stored —
// generous for any real Hawkwise payload (even with every lane/vehicle-
// class field populated it's well under this), just a backstop against a
// deliberately oversized authenticated push bloating the log.
const pushLogBodyMaxChars = 32 * 1024

// PushLogEntry is one logged attempt to call the push endpoint, newest
// first from ListPushLog.
type PushLogEntry struct {
	ID            int64  `json:"id"`
	ReceivedAt    string `json:"received_at"` // RFC3339Nano, UTC
	RemoteIP      string `json:"remote_ip"`
	AuthResult    string `json:"auth_result"` // ok | missing_api_key | invalid_api_key | rate_limited_ip | rate_limited_integration | bad_method | bad_content_type
	Integration   string `json:"integration,omitempty"`      // matched integration's name — only set when auth_result=ok
	HTTPStatus    int    `json:"http_status"`
	BodyValidJSON *bool  `json:"body_valid_json,omitempty"` // nil when auth failed before the body was ever read
	ParseError    string `json:"parse_error,omitempty"`
	Body          string `json:"body,omitempty"`
	BodyTruncated bool   `json:"body_truncated,omitempty"`
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
		INSERT INTO push_log (received_at, remote_ip, auth_result, integration, http_status, body_valid_json, parse_error, body, body_truncated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, e.ReceivedAt, e.RemoteIP, e.AuthResult, e.Integration, e.HTTPStatus, nullBoolInt(e.BodyValidJSON), e.ParseError, body, boolInt(truncated))
	if err != nil {
		log.Warn().Err(err).Msg("[trafficflow] logPushAttempt insert failed (continuing)")
		return
	}
	if _, err := db.Exec(`DELETE FROM push_log WHERE id NOT IN (SELECT id FROM push_log ORDER BY id DESC LIMIT ?)`, pushLogMaxRows); err != nil {
		log.Warn().Err(err).Msg("[trafficflow] push_log prune failed (continuing)")
	}
}

// ListPushLog returns every logged push attempt still retained, most
// recent first.
func ListPushLog() []PushLogEntry {
	rows, err := db.Query(`
		SELECT id, received_at, remote_ip, auth_result, integration, http_status, body_valid_json, parse_error, body, body_truncated
		FROM push_log ORDER BY id DESC
	`)
	if err != nil {
		log.Warn().Err(err).Msg("[trafficflow] ListPushLog query failed")
		return nil
	}
	defer rows.Close()

	out := make([]PushLogEntry, 0, pushLogMaxRows)
	for rows.Next() {
		var e PushLogEntry
		var validJSON sql.NullInt64
		var truncated int
		if err := rows.Scan(&e.ID, &e.ReceivedAt, &e.RemoteIP, &e.AuthResult, &e.Integration, &e.HTTPStatus, &validJSON, &e.ParseError, &e.Body, &truncated); err != nil {
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
