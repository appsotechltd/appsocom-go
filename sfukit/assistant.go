package sfukit

// The Appsocom AI Assistant, from your Go server.
//
//  1. Once: tell the assistant about your app (its tools and pages) with
//     Client.SetAssistantConfig.
//  2. For each signed-in person: Client.CreateAssistantToken, and hand the
//     token to their browser or app.
//  3. Answer the assistant's tool calls, as that person, at your toolsUrl:
//
//     tools := sfukit.NewAssistantTools(os.Getenv("APPSOCOM_WEBHOOK_SECRET"), map[string]sfukit.AssistantToolHandler{
//         "list_students": func(ctx context.Context, args json.RawMessage, call *sfukit.AssistantToolCall) (any, error) {
//             var in struct{ Cls string `json:"cls"` }
//             if err := json.Unmarshal(args, &in); err != nil {
//                 return nil, sfukit.NewAssistantToolError("cls is required")
//             }
//             return db.Students(ctx, call.Metadata["school"], in.Cls)
//         },
//     })
//     http.Handle("/assistant-tools", tools)

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// AssistantToolDefinition is one tool of your app, as the model sees it.
type AssistantToolDefinition struct {
	// Name: letters, numbers, _ and -, starting with a letter (max 64).
	Name string `json:"name"`
	// Description is written for the model: when to use it and what it returns.
	Description string `json:"description"`
	// Parameters is the JSON Schema of the arguments.
	Parameters map[string]any `json:"parameters,omitempty"`
	// Risk is "read" (the default), "write" or "destructive". Only read tools run today.
	Risk string `json:"risk,omitempty"`
	// Pinned tools are always offered, even when the project has many tools.
	Pinned bool `json:"pinned,omitempty"`
	// AssistantAllowed false hides the tool from the assistant (but keeps it in the list).
	AssistantAllowed *bool `json:"assistantAllowed,omitempty"`
	// CacheSeconds (read tools only) reuses a result for the same person and arguments (1-3600).
	CacheSeconds int `json:"cacheSeconds,omitempty"`
}

// AssistantAppPage is one page of your app the assistant can send people to.
type AssistantAppPage struct {
	Label string `json:"label"`
	// Path is the page's path in your app, starting with "/".
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
	// Aliases are other names people use for it ("bursary" for Fees).
	Aliases []string `json:"aliases,omitempty"`
}

// AssistantConfig tells the assistant about your app. SetAssistantConfig replaces the whole config.
type AssistantConfig struct {
	// Name is what the assistant calls itself.
	Name string `json:"name,omitempty"`
	// Instructions are your app's own rules and context, added after Appsocom's.
	Instructions string                    `json:"instructions,omitempty"`
	Tools        []AssistantToolDefinition `json:"tools,omitempty"`
	// ToolsURL is where tool calls are POSTed, signed (required when there are tools).
	ToolsURL string             `json:"toolsUrl,omitempty"`
	AppMap   []AssistantAppPage `json:"appMap,omitempty"`
	// DefaultTier is "standard", "smart" or "best".
	DefaultTier string `json:"defaultTier,omitempty"`
	// RoleTiers picks a tier per role, e.g. {"staff": "smart"}.
	RoleTiers map[string]string `json:"roleTiers,omitempty"`
	// Knowledge lets it search the project's knowledge base (default true).
	Knowledge *bool `json:"knowledge,omitempty"`
	// RetentionDays deletes a conversation this many days after its last message (1-730, default 90).
	RetentionDays int `json:"retentionDays,omitempty"`
}

// AssistantTokenRequest describes one signed-in person who may chat with the assistant.
type AssistantTokenRequest struct {
	UserID string `json:"userId"`
	// Name is shown to the model ("Name: …").
	Name string `json:"name,omitempty"`
	// Role picks the tier (RoleTiers) and goes back with every tool call.
	Role string `json:"role,omitempty"`
	// ConversationID continues that conversation; a new one starts when empty.
	ConversationID string `json:"conversationId,omitempty"`
	// Tools limits the person to these tool names.
	Tools []string `json:"tools,omitempty"`
	// Paths limits the app-map pages to those under these paths.
	Paths []string `json:"paths,omitempty"`
	// Tier overrides the role's tier.
	Tier          string `json:"tier,omitempty"`
	RetentionDays int    `json:"retentionDays,omitempty"`
	// MonthlyMessages caps this person's assistant messages per month (nil = no cap of their own).
	MonthlyMessages *int `json:"monthlyMessages,omitempty"`
	// Metadata is your own ids, passed back with every tool call (at most 2 KB).
	Metadata map[string]string `json:"metadata,omitempty"`
	// Context is what your app knows about this person that the assistant
	// should always have (their organisation, classes, children), at most
	// 3,000 characters.
	Context string `json:"context,omitempty"`
	// ReportUsage adds tier, billed units, cost and tokens to the chat's done
	// event, for a backend that relays answers (not for browser tokens).
	ReportUsage bool `json:"reportUsage,omitempty"`
	// TTLSeconds defaults to 3600, at most 43200.
	TTLSeconds int `json:"ttlSeconds,omitempty"`
}

// AssistantToken is a token for one person, the conversation it continues, and its expiry (unix seconds).
type AssistantToken struct {
	Token          string `json:"token"`
	ConversationID string `json:"conversationId"`
	ExpiresAt      int64  `json:"expiresAt"`
}

// SetAssistantConfig tells the assistant about your app: its tools, toolsUrl, pages and tiers.
// It replaces the whole config and returns the config as stored.
func (c *Client) SetAssistantConfig(ctx context.Context, config AssistantConfig) (*AssistantConfig, error) {
	body, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	var out struct {
		Config AssistantConfig `json:"config"`
	}
	if err := c.do(ctx, http.MethodPut, "/assistant/config", body, &out); err != nil {
		return nil, err
	}
	return &out.Config, nil
}

// AssistantConfig returns the project's assistant config.
func (c *Client) AssistantConfig(ctx context.Context) (*AssistantConfig, error) {
	var out struct {
		Config AssistantConfig `json:"config"`
	}
	if err := c.do(ctx, http.MethodGet, "/assistant/config", nil, &out); err != nil {
		return nil, err
	}
	return &out.Config, nil
}

// CreateAssistantToken mints a token for one signed-in person to chat with the
// assistant. Give it to their browser or app.
func (c *Client) CreateAssistantToken(ctx context.Context, req AssistantTokenRequest) (*AssistantToken, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var out AssistantToken
	if err := c.do(ctx, http.MethodPost, "/assistant/tokens", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteAssistantConversation forgets one person's conversation (its memory and transcript).
func (c *Client) DeleteAssistantConversation(ctx context.Context, userID, conversationID string) error {
	path, err := idPath(http.MethodDelete, "/assistant/conversations/%s/%s", userID, conversationID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// ---------- your toolsUrl ----------

// AssistantToolCall is what the assistant POSTs to your toolsUrl.
type AssistantToolCall struct {
	Type           string `json:"type"` // "tool_call"
	Kind           string `json:"kind"` // "assistant"
	ConversationID string `json:"conversationId"`
	UserID         string `json:"userId"`
	Role           string `json:"role,omitempty"`
	CallID         string `json:"callId"`
	Name           string `json:"name"`
	// Arguments is the JSON object the model chose ("{}" when it sent none).
	Arguments json.RawMessage `json:"arguments"`
	// Metadata is what you put in the person's token.
	Metadata map[string]string `json:"metadata"`
	// ApprovalID is set when the person confirmed this change on the card
	// (write, destructive and bulk tools). Make the change once per CallID.
	ApprovalID string `json:"approvalId,omitempty"`
	ApprovedAt int64  `json:"approvedAt,omitempty"`
}

// AssistantToolError is an error whose message is shown to the model. Any
// other error a tool returns is hidden as "The tool failed.".
type AssistantToolError struct{ Message string }

func (e *AssistantToolError) Error() string { return e.Message }

// NewAssistantToolError returns an error whose message the model sees.
func NewAssistantToolError(message string) error { return &AssistantToolError{Message: message} }

// AssistantToolHandler runs one tool as the person who asked. What it returns
// is sent to the model as JSON (keep it small: at most 16 KB is read); nil
// sends "done".
type AssistantToolHandler func(ctx context.Context, args json.RawMessage, call *AssistantToolCall) (any, error)

// AssistantTools is your toolsUrl: it checks each request's signature and
// runs the matching handler. It is an http.Handler.
type AssistantTools struct {
	secret   string
	handlers map[string]AssistantToolHandler
	// Tolerance is how long a signature stays valid (default 5 minutes).
	Tolerance time.Duration
	// OnError is called when a handler fails with anything other than an
	// *AssistantToolError (including a panic).
	OnError func(err error, call *AssistantToolCall)
}

// NewAssistantTools answers the assistant's tool calls. secret is your
// project's webhook secret. It panics when secret is empty.
func NewAssistantTools(secret string, handlers map[string]AssistantToolHandler) *AssistantTools {
	if secret == "" {
		panic("sfukit: NewAssistantTools needs your webhook secret")
	}
	hs := make(map[string]AssistantToolHandler, len(handlers))
	for k, v := range handlers {
		hs[k] = v
	}
	return &AssistantTools{secret: secret, handlers: hs, Tolerance: 5 * time.Minute}
}

var sigV1 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// VerifyAssistantToolCall checks a tool call's X-SfuKit-Signature against the
// raw body. It returns the call, or an error when the signature is wrong or
// older than tolerance, or the body is not an assistant tool call.
func VerifyAssistantToolCall(secret string, body []byte, signatureHeader string, tolerance time.Duration) (*AssistantToolCall, error) {
	if secret == "" || signatureHeader == "" {
		return nil, errors.New("sfukit: missing signature")
	}
	var ts, sig string
	for _, part := range strings.Split(signatureHeader, ",") {
		k, v, _ := strings.Cut(part, "=")
		switch strings.TrimSpace(k) {
		case "t":
			ts = strings.TrimSpace(v)
		case "v1":
			sig = strings.TrimSpace(v)
		}
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || !sigV1.MatchString(sig) {
		return nil, errors.New("sfukit: malformed signature")
	}
	if d := time.Duration(time.Now().Unix()-t) * time.Second; d > tolerance || d < -tolerance {
		return nil, errors.New("sfukit: signature too old")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(sig)) {
		return nil, errors.New("sfukit: bad signature")
	}
	var call AssistantToolCall
	if err := json.Unmarshal(body, &call); err != nil {
		return nil, fmt.Errorf("sfukit: bad tool call: %w", err)
	}
	if call.Type != "tool_call" || call.Kind != "assistant" || call.Name == "" {
		return nil, errors.New("sfukit: not an assistant tool call")
	}
	if len(call.Arguments) == 0 || string(call.Arguments) == "null" {
		call.Arguments = json.RawMessage("{}")
	}
	if call.Metadata == nil {
		call.Metadata = map[string]string{}
	}
	return &call, nil
}

// Handle checks, runs and answers one call. Pass the RAW body. It returns the
// HTTP status and the JSON body to send: {"result": ...} or {"error": ...}.
func (a *AssistantTools) Handle(ctx context.Context, body []byte, signatureHeader string) (int, []byte) {
	tol := a.Tolerance
	if tol <= 0 {
		tol = 5 * time.Minute
	}
	call, err := VerifyAssistantToolCall(a.secret, body, signatureHeader, tol)
	if err != nil {
		return http.StatusUnauthorized, mustJSON(map[string]any{"error": "bad signature"})
	}
	handler, ok := a.handlers[call.Name]
	if !ok || handler == nil {
		return http.StatusOK, mustJSON(map[string]any{"error": "There is no tool called " + call.Name + "."})
	}
	result, err := a.run(ctx, handler, call)
	if err == nil {
		if result == nil {
			result = "done"
		}
		out, merr := json.Marshal(map[string]any{"result": result})
		if merr == nil {
			return http.StatusOK, out
		}
		err = merr
	}
	var toolErr *AssistantToolError
	if errors.As(err, &toolErr) {
		return http.StatusOK, mustJSON(map[string]any{"error": toolErr.Message})
	}
	if a.OnError != nil {
		a.OnError(err, call)
	}
	return http.StatusOK, mustJSON(map[string]any{"error": "The tool failed."})
}

func (a *AssistantTools) run(ctx context.Context, handler AssistantToolHandler, call *AssistantToolCall) (result any, err error) {
	defer func() {
		if p := recover(); p != nil {
			result, err = nil, fmt.Errorf("sfukit: tool %s panicked: %v", call.Name, p)
		}
	}()
	return handler(ctx, call.Arguments, call)
}

// ServeHTTP makes AssistantTools your toolsUrl handler.
func (a *AssistantTools) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write(mustJSON(map[string]any{"error": "POST only"}))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write(mustJSON(map[string]any{"error": "could not read the body"}))
		return
	}
	status, out := a.Handle(r.Context(), body, r.Header.Get("X-SfuKit-Signature"))
	w.WriteHeader(status)
	w.Write(out)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
