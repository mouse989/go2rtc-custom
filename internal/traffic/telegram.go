package traffic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// applyTemplate replaces {key} tokens with values from vars.
func applyTemplate(tpl string, vars map[string]string) string {
	for k, v := range vars {
		tpl = strings.ReplaceAll(tpl, "{"+k+"}", v)
	}
	return tpl
}

// tgSendPhoto sends a PNG image with a caption to a Telegram chat.
func tgSendPhoto(token, chatID string, pngData []byte, caption string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", token)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("chat_id", chatID)
	_ = w.WriteField("parse_mode", "HTML")
	if caption != "" {
		_ = w.WriteField("caption", caption)
	}
	fw, err := w.CreateFormFile("photo", "map.png")
	if err != nil {
		return err
	}
	if _, err := fw.Write(pngData); err != nil {
		return err
	}
	w.Close()

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(url, w.FormDataContentType(), &body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("telegram sendPhoto error: %s", resp.Status)
	}
	return nil
}

// tgSendMessage sends an HTML-formatted message to a Telegram chat.
func tgSendMessage(token, chatID, html string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	body := map[string]string{
		"chat_id":    chatID,
		"text":       html,
		"parse_mode": "HTML",
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("telegram API error: %s", resp.Status)
	}
	return nil
}

// formatPointList formats points using tplPoint, splitting into chunks <=4000 chars.
func formatPointList(pts []Point, tplPoint string) []string {
	var chunks []string
	var buf strings.Builder

	for i, p := range pts {
		line := applyTemplate(tplPoint, map[string]string{
			"n":     fmt.Sprintf("%d", i+1),
			"jf":    fmt.Sprintf("%.1f", p.JamFactor),
			"label": p.Label,
			"area":  p.Area,
		})
		line += "\n"
		if buf.Len()+len(line) > 4000 {
			chunks = append(chunks, strings.TrimSpace(buf.String()))
			buf.Reset()
		}
		buf.WriteString(line)
	}
	if buf.Len() > 0 {
		chunks = append(chunks, strings.TrimSpace(buf.String()))
	}
	return chunks
}

// sendTelegramReport sends map images and/or text lists for raw/filtered/persistent jams.
func sendTelegramReport(cfg Config, raw, filtered, persistent []Point) error {
	tg := cfg.Telegram
	if !tg.Enabled || tg.Token == "" || tg.ChatID == "" {
		return nil
	}

	now := time.Now().Format("15:04:05 02/01/2006")
	regions := cfg.Regions

	vars := map[string]string{
		"time":   now,
		"minJam": fmt.Sprintf("%.1f", cfg.MinJam),
	}

	// Send header message first
	headerText := applyTemplate(tg.TplHeader, vars)
	if err := tgSendMessage(tg.Token, tg.ChatID, headerText); err != nil {
		return err
	}

	// Helper: send image then text list for a set of points
	sendSection := func(pts []Point, sendImg, sendList bool,
		tplSection, hexColor, mapTitle string, dotMode bool) error {

		sectionVars := map[string]string{
			"count": fmt.Sprintf("%d", len(pts)),
			"time":  now,
		}

		if sendImg {
			addLog("info", fmt.Sprintf("rendering map image: %s (%d points)", mapTitle, len(pts)))
			pngData, err := renderMapImage(pts, regions, mapTitle, hexColor, dotMode, 1000, 700)
			if err != nil {
				addLog("warn", fmt.Sprintf("render map %s: %v", mapTitle, err))
			} else {
				caption := applyTemplate(tplSection, sectionVars)
				if len(caption) > 1024 {
					caption = caption[:1024]
				}
				if err := tgSendPhoto(tg.Token, tg.ChatID, pngData, caption); err != nil {
					addLog("warn", fmt.Sprintf("sendPhoto %s: %v", mapTitle, err))
				}
			}
		}

		if sendList && len(pts) > 0 {
			header := applyTemplate(tplSection, sectionVars)
			chunks := formatPointList(pts, tg.TplPoint)
			first := header
			if len(chunks) > 0 {
				first += "\n" + chunks[0]
			}
			if err := tgSendMessage(tg.Token, tg.ChatID, first); err != nil {
				return err
			}
			for _, chunk := range chunks[1:] {
				if err := tgSendMessage(tg.Token, tg.ChatID, chunk); err != nil {
					return err
				}
			}
		}
		return nil
	}

	// ── Raw (large labeled circles, fixed color) ───────────────────────
	if tg.SendRaw || tg.SendRawImg {
		if err := sendSection(raw, tg.SendRawImg, tg.SendRaw,
			tg.TplRaw, "#94a3b8", "Raw Data", false); err != nil {
			return err
		}
	}

	// ── Filtered (large labeled circles, blue) ─────────────────────────
	if tg.SendFiltered || tg.SendFilteredImg {
		if err := sendSection(filtered, tg.SendFilteredImg, tg.SendFiltered,
			tg.TplFiltered, "#60a5fa", "Filtered", false); err != nil {
			return err
		}
	}

	// ── Persistent (small dots colored by jam factor) ──────────────────
	if tg.SendPersistent || tg.SendPersistentImg {
		if err := sendSection(persistent, tg.SendPersistentImg, tg.SendPersistent,
			tg.TplPersistent, "#f97316", "Persistent", true); err != nil {
			return err
		}
	}

	return nil
}
