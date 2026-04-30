package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const draftModel = "@cf/google/gemma-4-26b-a4b-it"

type DraftVariant struct {
	Title string   `json:"title"`
	Body  string   `json:"body"`
	Tags  []string `json:"tags"`
}

// draftSystem is the model contract. Tweak this string and the entire
// vibe of AI-drafted posts changes — keep it tight, opinionated, and
// loud about the "no names" rule because the model will obey if asked.
const draftSystem = `you write short anonymous campus-forum posts in the voice of an indian college student. follow every rule:

- lowercase only, no emojis, no markdown
- no real names of people, professors, companies, brands, products, places, or apps. if the user mentions one, replace with a generic description (eg "the OS prof", "a guy from cs-a", "a tier-1 product company")
- title under 80 chars, body 30-80 words
- end the body with a real question that invites replies
- casual student tone, sparing use of "ngl", "tbh", "honestly", "anyone else", "wait"
- the user is asking, not advising

return only a json array of two variants — different angles on the same situation:
[
  {"title": "...", "body": "...", "tags": ["...", "..."]},
  {"title": "...", "body": "...", "tags": ["...", "..."]}
]
each tag is one lowercase word or hyphenated phrase. 2-4 tags per variant. no other text outside the array.`

// Draft asks the LLM to turn a 1-line user dilemma into 2 ready-to-post
// variants. Returns ErrNotConfigured if the client wasn't set up; any
// other error means the model failed to produce parseable JSON and the
// caller should fall back to "compose by hand."
func (c *Client) Draft(ctx context.Context, dilemma string) ([]DraftVariant, error) {
	if c == nil {
		return nil, ErrNotConfigured
	}
	body, _ := json.Marshal(map[string]any{
		"messages": []map[string]string{
			{"role": "system", "content": draftSystem},
			{"role": "user", "content": dilemma},
		},
		"max_tokens":  600,
		"temperature": 0.8,
	})
	url := "https://api.cloudflare.com/client/v4/accounts/" + c.accountID +
		"/ai/run/" + draftModel
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("draft: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("draft: cloudflare returned %d: %s",
			resp.StatusCode, truncate(string(b), 240))
	}

	var parsed struct {
		Result struct {
			Response string `json:"response"`
		} `json:"result"`
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("draft: decode: %w", err)
	}
	if !parsed.Success {
		return nil, fmt.Errorf("draft: cloudflare returned success=false")
	}

	// the model occasionally wraps the JSON in prose ("here's your variants:")
	// — extract the first [ ... ] balanced span before unmarshalling.
	raw := parsed.Result.Response
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("draft: no json array in model output: %s",
			truncate(raw, 240))
	}
	var variants []DraftVariant
	if err := json.Unmarshal([]byte(raw[start:end+1]), &variants); err != nil {
		return nil, fmt.Errorf("draft: parse variants: %w (raw=%s)",
			err, truncate(raw, 240))
	}
	if len(variants) == 0 {
		return nil, fmt.Errorf("draft: model returned empty variants array")
	}
	return variants, nil
}
