package sfukit

// The Appsocom Speech service, from your Go server.
//
//	audio, err := kit.TextToSpeech(ctx, sfukit.TextToSpeechRequest{Text: "Good morning, class", Language: "en"})
//	link, err := kit.TextToSpeechURL(ctx, sfukit.TextToSpeechRequest{Text: "Ẹ káàárọ̀", Language: "yo"}, time.Hour)
//	out, err := kit.SpeechToText(ctx, file, sfukit.SpeechToTextOptions{ContentType: "audio/webm", Language: "en"})
//
// Text to speech is cached by the server: the same text, voice and format
// again comes from the cache and is not charged.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TextToSpeechRequest is what to speak.
type TextToSpeechRequest struct {
	// Text: up to 5,000 characters.
	Text string `json:"text"`
	// Language: default "en". Yoruba ("yo"), Hausa ("ha") and Igbo ("ig") use Spitch or ElevenLabs.
	Language string `json:"language,omitempty"`
	// Voice: a voice id of the vendor that speaks (default: its default voice for the language).
	Voice string `json:"voice,omitempty"`
	// Provider: "cartesia", "elevenlabs", "spitch", "openai" or "deepgram" (default: chosen by language).
	Provider string `json:"provider,omitempty"`
	// Format: "mp3" (default) or "wav".
	Format string `json:"format,omitempty"`
	// Speed: 0.5 to 2 (0 = normal).
	Speed float64 `json:"speed,omitempty"`
	Model string  `json:"model,omitempty"`
	// NoCache: always make the audio fresh (and always billed).
	NoCache bool `json:"-"`
}

// TextToSpeechLink is a signed link to spoken audio, playable without the key until ExpiresAt.
type TextToSpeechLink struct {
	URL        string `json:"url"`
	ExpiresAt  int64  `json:"expiresAt"` // milliseconds since 1970
	Cached     bool   `json:"cached"`
	Characters int    `json:"characters"` // billed (0 when served from the cache)
	Provider   string `json:"provider"`
	Voice      string `json:"voice"`
	Format     string `json:"format"`
}

// SpeechToTextOptions describes a recording to transcribe.
type SpeechToTextOptions struct {
	// ContentType, e.g. "audio/webm", "audio/wav". Default "application/octet-stream".
	ContentType string
	// Language: empty = detected (Deepgram, ElevenLabs).
	Language string
	// Provider: "deepgram", "elevenlabs" or "spitch" (default: chosen by language).
	Provider string
	// Model: Deepgram model (default nova-3).
	Model string
}

// SpeechWord is one word's timing in a transcript.
type SpeechWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// SpeechToTextResult is a transcript.
type SpeechToTextResult struct {
	Text     string       `json:"text"`
	Language string       `json:"language,omitempty"`
	Seconds  float64      `json:"seconds"` // billed: the recording's length, rounded up
	Provider string       `json:"provider"`
	Words    []SpeechWord `json:"words,omitempty"`
}

// SpeechUsage is the project's speech spending per day and today's cap.
type SpeechUsage struct {
	Today struct {
		Day      string  `json:"day"`
		SpentUSD float64 `json:"spentUsd"`
		CapUSD   float64 `json:"capUsd"`
	} `json:"today"`
	Days []struct {
		Day     string  `json:"day"`
		CostUSD float64 `json:"costUsd"`
	} `json:"days"`
}

func ttsBody(req TextToSpeechRequest, response string, expires time.Duration) ([]byte, error) {
	m := map[string]any{}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["response"] = response
	if req.NoCache {
		m["cache"] = false
	}
	if expires > 0 {
		m["expiresIn"] = int(expires / time.Second)
	}
	return json.Marshal(m)
}

// TextToSpeech speaks req.Text and returns the audio file (mp3 unless Format "wav").
func (c *Client) TextToSpeech(ctx context.Context, req TextToSpeechRequest) ([]byte, error) {
	body, err := ttsBody(req, "audio", 0)
	if err != nil {
		return nil, err
	}
	return c.raw(ctx, "/speech/tts", "application/json", bytes.NewReader(body), 25<<20)
}

// TextToSpeechURL speaks req.Text and returns a signed link to the audio that works
// for `expires` (60 seconds to 7 days; 0 = one hour).
func (c *Client) TextToSpeechURL(ctx context.Context, req TextToSpeechRequest, expires time.Duration) (*TextToSpeechLink, error) {
	body, err := ttsBody(req, "url", expires)
	if err != nil {
		return nil, err
	}
	var out TextToSpeechLink
	if err := c.do(ctx, http.MethodPost, "/speech/tts", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SpeechToText transcribes a recording (up to 25 MB). Use a context with a
// generous deadline: long recordings take a while.
func (c *Client) SpeechToText(ctx context.Context, audio io.Reader, opts SpeechToTextOptions) (*SpeechToTextResult, error) {
	q := url.Values{}
	if opts.Language != "" {
		q.Set("language", opts.Language)
	}
	if opts.Provider != "" {
		q.Set("provider", opts.Provider)
	}
	if opts.Model != "" {
		q.Set("model", opts.Model)
	}
	path := "/speech/stt"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	ct := opts.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	data, err := c.raw(ctx, path, ct, audio, 1<<20)
	if err != nil {
		return nil, err
	}
	var out SpeechToTextResult
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SpeechUsage returns the project's speech spending per day and today's cap.
func (c *Client) SpeechUsage(ctx context.Context) (*SpeechUsage, error) {
	var out SpeechUsage
	if err := c.do(ctx, http.MethodGet, "/speech/usage", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetSpeechDailyBudget sets the project's own daily speech cap in US dollars
// (nil = the server's default; it can never go above the server's).
func (c *Client) SetSpeechDailyBudget(ctx context.Context, usd *float64) error {
	body, err := json.Marshal(map[string]any{"dailyBudgetUsd": usd})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, "/speech/config", body, nil)
}

// raw POSTs a body of any type and returns the answer's bytes. It uses the
// client's transport but not its timeout: speech calls can take longer than
// the usual admin call, so the context decides.
func (c *Client) raw(ctx context.Context, path, contentType string, body io.Reader, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.AdminKey)
	req.Header.Set("Content-Type", contentType)
	hc := http.DefaultClient
	if c.HTTPClient != nil {
		cp := *c.HTTPClient
		cp.Timeout = 0
		hc = &cp
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, &APIError{Method: http.MethodPost, Path: path, Status: res.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	return data, nil
}
