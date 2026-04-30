package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

const draftModel = "@cf/google/gemma-3-12b-it"

type DraftVariant struct {
	Title string   `json:"title"`
	Body  string   `json:"body"`
	Tags  []string `json:"tags"`
}

// draftSystem is the model contract. Tweak this string and the entire
// vibe of AI-drafted posts changes — keep it tight, opinionated, and
// loud about the "no names" rule because the model will obey if asked.
const draftSystem = `you are a normal gen-z draft generator for an anonymous indian campus forum. write one short post and stop. do not overanalyze. do not make a constraint checklist. do not explore multiple scenarios. just write the draft.

rules:
- lowercase only, no emojis, no markdown
- no real names of people, professors, companies, brands, products, places, or apps. swap real names for generic descriptions like "the OS prof" or "a tier-1 product company".
- title: 4-8 words, punchy
- body: 1-2 sentences, 15-25 words total
- end with one real question that invites replies
- casual student tone — drop "ngl", "tbh", "honestly", "anyone else", "wait" sparingly

return only this exact json array, with ONE object:
[
  {"title": "...", "body": "...", "tags": ["...", "..."]}
]
1-2 lowercase or hyphenated tags MAX. terminal UI — tag rows can't wrap. no other text outside the array.`

// Draft asks the LLM to turn a 1-line user dilemma into 2 ready-to-post
// variants. Returns ErrNotConfigured if the client wasn't set up; any
// other error means the model failed to produce parseable JSON and the
// caller should fall back to "compose by hand."
func (c *Client) Draft(ctx context.Context, dilemma string) ([]DraftVariant, error) {
	if c == nil {
		return nil, ErrNotConfigured
	}
	slog.Info("draft start", "model", draftModel, "dilemma_len", len(dilemma))
	body, _ := json.Marshal(map[string]any{
		"messages": []map[string]string{
			{"role": "system", "content": draftSystem},
			{"role": "user", "content": dilemma},
		},
		// gemma-3-12b-it is non-reasoning — emits ~50 tokens of JSON
		// directly in 1-2s. 600 is generous headroom.
		"max_tokens":  600,
		"temperature": 0.8,
	})
	url := "https://api.cloudflare.com/client/v4/accounts/" + c.accountID +
		"/ai/run/" + draftModel
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		slog.Error("draft new request", "err", err)
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		slog.Error("draft http", "err", err)
		return nil, fmt.Errorf("draft: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		slog.Error("draft non-200",
			"status", resp.StatusCode,
			"body", truncate(string(b), 480),
			"model", draftModel)
		return nil, fmt.Errorf("draft: cloudflare returned %d: %s",
			resp.StatusCode, truncate(string(b), 240))
	}

	// CF Workers AI returns two different shapes depending on the model:
	//   - Legacy flat format (llama-3.1, older models): result.response is a string
	//   - OpenAI Chat Completions (gemma-4, llama-4, gpt-oss, kimi, etc.):
	//     result.choices[0].message.content holds the string
	// We read both. raw becomes whichever one is populated.
	var parsed struct {
		Result struct {
			Response string `json:"response"`
			Choices  []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		} `json:"result"`
		Success bool  `json:"success"`
		Errors  []any `json:"errors"`
	}
	rawBody, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		slog.Error("draft decode", "err", err, "body", truncate(string(rawBody), 480))
		return nil, fmt.Errorf("draft: decode: %w", err)
	}
	if !parsed.Success {
		slog.Error("draft success=false",
			"errors", parsed.Errors,
			"body", truncate(string(rawBody), 480))
		return nil, fmt.Errorf("draft: cloudflare returned success=false")
	}

	// the model occasionally wraps the JSON in prose ("here's your variants:")
	// — extract the first [ ... ] balanced span before unmarshalling.
	// CF Workers AI returns the text in different fields depending on the
	// model: older flat shape uses result.response, newer OpenAI-compatible
	// shape (gemma-4, llama-4, gpt-oss, kimi, etc.) uses
	// result.choices[0].message.content. Fall back to choices when response
	// is empty so a single parser handles both.
	raw := parsed.Result.Response
	if raw == "" && len(parsed.Result.Choices) > 0 {
		raw = parsed.Result.Choices[0].Message.Content
	}
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start < 0 || end <= start {
		// Dump the FULL CF body, not just result.response — if the model
		// puts content in a different field (reasoning, choices, etc.)
		// we need to see it to know what to parse.
		slog.Error("draft no json array",
			"response", truncate(raw, 480),
			"raw_body", truncate(string(rawBody), 1200))
		return nil, fmt.Errorf("draft: no json array in model output: %s",
			truncate(raw, 240))
	}
	var variants []DraftVariant
	if err := json.Unmarshal([]byte(raw[start:end+1]), &variants); err != nil {
		slog.Error("draft parse variants",
			"err", err,
			"response", truncate(raw, 480))
		return nil, fmt.Errorf("draft: parse variants: %w (raw=%s)",
			err, truncate(raw, 240))
	}
	if len(variants) == 0 {
		slog.Error("draft empty variants", "response", truncate(raw, 480))
		return nil, fmt.Errorf("draft: model returned empty variants array")
	}
	slog.Info("draft ok", "variants", len(variants))
	return variants, nil
}
