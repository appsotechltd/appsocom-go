package sfukit

// AI Tasks (called AI Text in code): one Claude call from your Go server (POST /ai/complete).
// See https://docs.appsocom.dev/ai-tasks/
//
//	res, err := client.AITask(ctx, "mark", map[string]any{
//		"question": q, "answer": a, "rubric": rubric, "maxScore": 10,
//	}, &sfukit.AIOptions{User: studentID})
//	var mark struct{ Score float64 `json:"score"`; Feedback string `json:"feedback"` }
//	_ = res.Decode(&mark)

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// AITimeout is how long an AI Text call may take (it writes a whole answer).
var AITimeout = 5 * time.Minute

// AIOptions are the optional parts of an AI Text call.
type AIOptions struct {
	// Tier: "standard" (default, Claude Haiku 5.5), "smart" (Sonnet 5.5) or "best" (Opus 5.5).
	Tier string `json:"tier,omitempty"`
	// MaxTokens is the longest answer, thinking included (default 4000, at most 32000).
	MaxTokens int `json:"maxTokens,omitempty"`
	// User is your id for the person this is for (per-person monthly allowance, audit).
	User string `json:"user,omitempty"`
	// Label is your own name for the call, shown in usage and audit (e.g. "essay_marking").
	Label string `json:"label,omitempty"`
}

// AIMessage is one turn of a custom conversation.
type AIMessage struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// AICompleteRequest is your own prompt: Prompt, or System and Messages.
type AICompleteRequest struct {
	AIOptions
	Prompt   string      `json:"prompt,omitempty"`
	System   string      `json:"system,omitempty"`
	Messages []AIMessage `json:"messages,omitempty"`
	// JSON: true for any JSON object, or a JSON Schema (map with "type": "object") the answer keeps to.
	JSON any `json:"json,omitempty"`
}

// AIUsage is what one call used and costs (1 credit = $0.001 of model cost).
type AIUsage struct {
	Model            string  `json:"model"`
	InputTokens      int     `json:"inputTokens"`
	OutputTokens     int     `json:"outputTokens"`
	CacheReadTokens  int     `json:"cacheReadTokens"`
	CacheWriteTokens int     `json:"cacheWriteTokens"`
	CostUSD          float64 `json:"costUsd"`
	Credits          float64 `json:"credits"`
}

// AIResult is an AI Text answer. JSON holds the parsed answer of a JSON task
// or a JSON request ("null" when it couldn't be read; Error says why).
type AIResult struct {
	Task       string          `json:"task"`
	Tier       string          `json:"tier"`
	Text       string          `json:"text"`
	JSON       json.RawMessage `json:"json,omitempty"`
	Error      string          `json:"error,omitempty"`
	StopReason string          `json:"stopReason"`
	Usage      AIUsage         `json:"usage"`
}

// Decode reads the JSON answer into v.
func (r *AIResult) Decode(v any) error {
	if len(r.JSON) == 0 || string(r.JSON) == "null" {
		if r.Error != "" {
			return errors.New(r.Error)
		}
		return errors.New("sfukit: the answer has no JSON")
	}
	return json.Unmarshal(r.JSON, v)
}

// AIComplete makes one call with your own prompt.
func (c *Client) AIComplete(ctx context.Context, req AICompleteRequest) (*AIResult, error) {
	return c.aiCall(ctx, req)
}

// AITask runs a ready-made task: write, mark, questions, summary, moderate or extract.
func (c *Client) AITask(ctx context.Context, task string, input map[string]any, opts *AIOptions) (*AIResult, error) {
	body := struct {
		AIOptions
		Task  string         `json:"task"`
		Input map[string]any `json:"input"`
	}{Task: task, Input: input}
	if opts != nil {
		body.AIOptions = *opts
	}
	return c.aiCall(ctx, body)
}

// AISettings are AI Text's caps and choices for the project.
type AISettings struct {
	DailyBudgetUSD *float64 `json:"dailyBudgetUsd,omitempty"`
	UserMonthlyUSD *float64 `json:"userMonthlyUsd,omitempty"`
	Tiers          []string `json:"tiers,omitempty"`
	DefaultTier    string   `json:"defaultTier,omitempty"`
	Audit          *bool    `json:"audit,omitempty"`
}

// SetAISettings replaces the project's AI Text settings and returns the daily cap in force.
func (c *Client) SetAISettings(ctx context.Context, s AISettings) (float64, error) {
	body, err := json.Marshal(s)
	if err != nil {
		return 0, err
	}
	var out struct {
		DailyCapUSD float64 `json:"dailyCapUsd"`
	}
	if err := c.do(ctx, http.MethodPut, "/ai/settings", body, &out); err != nil {
		return 0, err
	}
	return out.DailyCapUSD, nil
}

// AIUsageReport returns spending (today against the cap) and calls by task and model, as JSON.
func (c *Client) AIUsageReport(ctx context.Context, days int) (json.RawMessage, error) {
	var out json.RawMessage
	q := url.Values{}
	if days > 0 {
		q.Set("days", strconv.Itoa(days))
	}
	path := "/ai/usage"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) aiCall(ctx context.Context, v any) (*AIResult, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	// The usual client times out after 10s; an answer can take minutes.
	ai := *c
	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	long := *hc
	if long.Timeout != 0 && long.Timeout < AITimeout {
		long.Timeout = AITimeout
	}
	ai.HTTPClient = &long
	var out AIResult
	if err := ai.do(ctx, http.MethodPost, "/ai/complete", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
