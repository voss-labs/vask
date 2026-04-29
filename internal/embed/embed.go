// Package embed wraps Cloudflare Workers AI's bge-m3 endpoint into a
// minimal Go client suitable for storing dense vectors alongside posts.
//
// We picked bge-m3 because it's the cheapest tier on CF's pricing table
// (1075 neurons per million input tokens, tied with qwen3-0.6b but more
// battle-tested), it's multilingual — important for Indian-campus users
// who code-switch between English and Hindi/Hinglish — and it produces
// 1024-dim dense vectors that drop straight into a libsql `F32_BLOB(1024)`
// column.
//
// The whole package intentionally stays at ~80 lines: one HTTP POST, one
// JSON unmarshal, one little-endian pack helper. No SDK, no provider
// abstraction — if we ever swap to local ONNX inference, the only Go
// surface that changes is `Embed`.
package embed

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// VectorDim is bge-m3's dense output dimensionality. Matches the
// `F32_BLOB(VectorDim)` column the migration creates.
const VectorDim = 1024

// modelEndpoint is the slug Cloudflare exposes bge-m3 under.
const modelEndpoint = "@cf/baai/bge-m3"

// Client is a thin Cloudflare Workers AI client scoped to the embedding
// model. nil-safe — calling Embed on a nil client returns ErrNotConfigured
// so callers can use a single code path regardless of whether the env
// has the credentials.
type Client struct {
	accountID string
	token     string
	httpc     *http.Client
}

// ErrNotConfigured signals that the env didn't supply CF credentials, so
// embeddings are silently disabled. Callers should treat this as
// "embedding not available, fall back to whatever non-vector path you
// have" — never as an error worth surfacing to the user.
var ErrNotConfigured = errors.New("embed: cloudflare credentials not set")

// FromEnv reads CF_ACCOUNT_ID and CF_AI_TOKEN. Returns nil (not an error)
// when either is missing, so cmd/vask can wire the client unconditionally
// and let local dev run without keys.
func FromEnv() *Client {
	aid := os.Getenv("CF_ACCOUNT_ID")
	tok := os.Getenv("CF_AI_TOKEN")
	if aid == "" || tok == "" {
		return nil
	}
	return &Client{
		accountID: aid,
		token:     tok,
		httpc:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Embed sends `text` to bge-m3 and returns the dense vector. Caller is
// expected to pass the title + body concatenated; chunking longer inputs
// is bge-m3's job, not ours.
func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	if c == nil {
		return nil, ErrNotConfigured
	}
	body, _ := json.Marshal(map[string]any{"text": text})
	url := "https://api.cloudflare.com/client/v4/accounts/" + c.accountID +
		"/ai/run/" + modelEndpoint
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embed: cloudflare returned %d: %s",
			resp.StatusCode, truncate(string(b), 240))
	}

	var parsed struct {
		Result struct {
			Data [][]float32 `json:"data"`
		} `json:"result"`
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("embed: decode: %w", err)
	}
	if !parsed.Success || len(parsed.Result.Data) == 0 {
		return nil, errors.New("embed: empty result from cloudflare")
	}
	v := parsed.Result.Data[0]
	if len(v) != VectorDim {
		return nil, fmt.Errorf("embed: got %d dims, want %d", len(v), VectorDim)
	}
	return v, nil
}

// Pack serialises a 1024-float vector to a 4096-byte little-endian blob,
// the wire format libsql expects for F32_BLOB columns. We never store the
// vector as a JSON string — that doubles the size and forces a parse on
// every read.
func Pack(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// Format renders a vector as the "[v1,v2,...]" string libsql's vector()
// SQL function accepts. Used at query time to compare a search-query
// vector against stored post embeddings, since libsql doesn't accept
// raw blobs as the second argument to vector_distance_cos. Trailing
// zero-precision is fine — bge-m3 outputs 32-bit precision anyway.
func Format(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 8)
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
