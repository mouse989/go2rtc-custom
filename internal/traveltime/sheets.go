package traveltime

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"
)

// pushToGoogleSheet posts a batch log to the Google Apps Script webhook.
func pushToGoogleSheet(scriptURL, timestamp string, entries []LogEntry) {
	type logPayload struct {
		Action    string     `json:"action"`
		Timestamp string     `json:"timestamp"`
		Logs      []LogEntry `json:"logs"`
	}
	body, err := json.Marshal(logPayload{
		Action:    "log_traffic",
		Timestamp: timestamp,
		Logs:      entries,
	})
	if err != nil {
		log.Error().Err(err).Msg("[traveltime] sheets: marshal failed")
		return
	}

	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Post(scriptURL, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Warn().Err(err).Msg("[traveltime] sheets: push failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Warn().Int("status", resp.StatusCode).Msg("[traveltime] sheets: non-2xx response")
		return
	}
	log.Debug().Int("count", len(entries)).Msg("[traveltime] sheets: batch pushed")
}
