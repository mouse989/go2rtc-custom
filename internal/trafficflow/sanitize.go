package trafficflow

// sanitize.go — defensive JSON recovery for the Hawkwise push endpoint.
//
// Investigated after an admin reported that pushes from a camera they had
// already mapped to a Sensor kept landing in the push log's "unknown
// source" bucket — the one reserved for requests whose source_uuid was
// never captured at all (rejected before auth, or body genuinely
// unparsable). The sample body they extracted from one such entry was,
// when hand-copied into a text file, perfectly valid JSON matching this
// endpoint's schema in full, source_uuid included — which should be
// impossible for a request that actually logged as "unknown source"
// unless the RAW bytes on the wire differed from what they copied.
//
// The likely culprit: avg_speed fields here are computed by the camera as
// distance/time, which is 0/0 (undefined) whenever a vehicle class has
// zero volume — several classes in the very sample provided had volume 0.
// Many embedded C/Python JSON encoders serialize that as the bare token
// NaN (or Infinity for a different edge case) without validating the
// result is legal JSON — Go's encoding/json correctly rejects it outright
// ("invalid character 'N' looking for beginning of value"), failing
// json.Unmarshal for the ENTIRE push even though every other field,
// including source_uuid, was fine. A human reading/retyping the body
// later would naturally "fix" the invalid token without noticing, which
// is exactly what would make a copied sample look valid while the
// original request did not parse.
//
// sanitizeNonFiniteJSON rewrites bare NaN/Infinity/-Infinity tokens (never
// ones inside a quoted string) to 0 so the rest of the payload can still be
// parsed and, in particular, ingested — recovering flow records that would
// otherwise be silently dropped, not just fixing how they're logged.
// bestEffortSourceUUID is a last-resort fallback for any other malformed
// body this doesn't fix: a plain regex scan for a source_uuid-shaped value,
// used only to label a push log entry for debugging — it is never used to
// decide where a record gets stored.

import (
	"encoding/json"
	"regexp"
)

// nonFiniteTokenRe matches a JSON value position (right after a ':', ',' or
// '[') immediately followed by a bare NaN/Infinity/-Infinity token. The
// punctuation+whitespace prefix is captured so it can be preserved — only
// the illegal token itself is replaced.
var nonFiniteTokenRe = regexp.MustCompile(`([:\[,]\s*)-?(?:NaN|Infinity)\b`)

// sanitizeNonFiniteJSON returns body with every bare NaN/Infinity/-Infinity
// token replaced by 0, or nil if no such token is present (letting the
// caller distinguish "nothing to fix" from "fixed, try again").
func sanitizeNonFiniteJSON(body []byte) []byte {
	if !nonFiniteTokenRe.Match(body) {
		return nil
	}
	return nonFiniteTokenRe.ReplaceAll(body, []byte("${1}0"))
}

// sourceUUIDFieldRe matches "source_uuid": "<value>" however it's nested —
// deliberately loose (any string-ish token, not just hex-with-dashes) since
// this only has to work as a debugging aid for push-log grouping, never for
// deciding what gets ingested.
var sourceUUIDFieldRe = regexp.MustCompile(`"source_uuid"\s*:\s*"([^"]{1,128})"`)

// bestEffortSourceUUID scans raw, still-unparsable body bytes for a
// source_uuid value via plain text search, so a push log entry can still be
// grouped under the right camera even when the body doesn't parse at all
// (e.g. an unanticipated wrapper shape) instead of falling into "unknown
// source" and hiding a real, identifiable camera among genuine pre-auth
// rejections.
func bestEffortSourceUUID(body []byte) string {
	m := sourceUUIDFieldRe.FindSubmatch(body)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// parseHawkwisePush unmarshals a push body, retrying once against a
// sanitized copy (see sanitizeNonFiniteJSON) if the first attempt fails.
// recovered reports whether the sanitized retry is what succeeded, so the
// caller can note it in the push log for anyone debugging why a push that
// "looks fine" needed a fix applied. On total failure req is the zero value
// and err is the error from the ORIGINAL (unsanitized) attempt — the most
// accurate one to show the caller, since the sanitized copy is an internal
// recovery attempt, not what was actually received.
func parseHawkwisePush(body []byte) (req hawkwisePush, err error, recovered bool) {
	if err = json.Unmarshal(body, &req); err == nil {
		return req, nil, false
	}
	original := err
	if cleaned := sanitizeNonFiniteJSON(body); cleaned != nil {
		var retry hawkwisePush
		if err2 := json.Unmarshal(cleaned, &retry); err2 == nil {
			return retry, nil, true
		}
	}
	return hawkwisePush{}, original, false
}
