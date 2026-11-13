// Package codexhistory contains the shared read-only rollout message rules.
package codexhistory

import (
	"encoding/json"
	"strings"
)

type Record struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		ID        string `json:"id"`
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
		Type      string `json:"type"`
		Role      string `json:"role"`
		Message   string `json:"message"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Input     string `json:"input"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"payload"`
}

func Parse(line []byte) (Record, error) { var r Record; err := json.Unmarshal(line, &r); return r, err }
func (r Record) SessionID() string {
	if r.Payload.ID != "" {
		return r.Payload.ID
	}
	return r.Payload.SessionID
}
func UserText(s string) string {
	const marker = "## My request for Codex:"
	if i := strings.LastIndex(s, marker); i >= 0 {
		s = s[i+len(marker):]
	}
	return strings.TrimSpace(s)
}

// Conversation deliberately ignores response_item user/developer context and
// event_msg agent_message duplicates, matching the desktop session viewer.
func (r Record) Conversation() (role, text string) {
	p := r.Payload
	if r.Type == "event_msg" && p.Type == "user_message" {
		return "user", UserText(p.Message)
	}
	if r.Type == "response_item" && p.Type == "message" && p.Role == "assistant" {
		var texts []string
		for _, c := range p.Content {
			if c.Text != "" {
				texts = append(texts, c.Text)
			}
		}
		return "assistant", strings.Join(texts, "\n")
	}
	return "", ""
}
