package sfukit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A tool call signed by the Worker's own signWebhook (packages/server/src/webhooks.ts), shared by every SDK's tests.
const (
	assistantVectorSecret    = "whsec_assistant_test"
	assistantVectorBody      = `{"type":"tool_call","kind":"assistant","conversationId":"c1","userId":"u_7","role":"teacher","callId":"call_1","name":"list_students","arguments":{"cls":"JSS1"},"metadata":{"school":"s1"}}`
	assistantVectorSignature = "t=1763500000,v1=6497035b3915fa61b8deb6bfa54b0f22d52ff62c71f077a2d43873d747ed24a9"
)

func TestVerifyAssistantToolCallWorkerVector(t *testing.T) {
	if _, err := VerifyAssistantToolCall(assistantVectorSecret, []byte(assistantVectorBody), assistantVectorSignature, 5*time.Minute); err == nil {
		t.Fatal("stale signature accepted")
	}
	forever := 1 << 62 * time.Nanosecond
	call, err := VerifyAssistantToolCall(assistantVectorSecret, []byte(assistantVectorBody), assistantVectorSignature, forever)
	if err != nil {
		t.Fatal(err)
	}
	if call.Name != "list_students" || string(call.Arguments) != `{"cls":"JSS1"}` || call.Metadata["school"] != "s1" || call.Role != "teacher" {
		t.Fatalf("call = %+v", call)
	}
	if _, err := VerifyAssistantToolCall("other", []byte(assistantVectorBody), assistantVectorSignature, forever); err == nil {
		t.Fatal("wrong secret accepted")
	}
}

func toolCallBody(name string, args string, edit func(map[string]any)) []byte {
	m := map[string]any{"type": "tool_call", "kind": "assistant", "conversationId": "c1", "userId": "u_7", "role": "teacher", "callId": "call_1", "name": name, "arguments": json.RawMessage(args), "metadata": map[string]string{"school": "s1"}}
	if edit != nil {
		edit(m)
	}
	b, _ := json.Marshal(m)
	return b
}

func TestAssistantTools(t *testing.T) {
	var failures []string
	tools := NewAssistantTools("s", map[string]AssistantToolHandler{
		"list_students": func(ctx context.Context, args json.RawMessage, call *AssistantToolCall) (any, error) {
			var in struct {
				Cls string `json:"cls"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, err
			}
			return []map[string]string{{"name": "Ada", "cls": in.Cls, "school": call.Metadata["school"], "asker": call.UserID}}, nil
		},
		"nothing": func(context.Context, json.RawMessage, *AssistantToolCall) (any, error) { return nil, nil },
		"denied": func(context.Context, json.RawMessage, *AssistantToolCall) (any, error) {
			return nil, NewAssistantToolError("You can only see your own classes.")
		},
		"boom": func(context.Context, json.RawMessage, *AssistantToolCall) (any, error) {
			return nil, errors.New("database password is hunter2")
		},
		"panics": func(context.Context, json.RawMessage, *AssistantToolCall) (any, error) { panic("oops") },
	})
	tools.OnError = func(err error, call *AssistantToolCall) { failures = append(failures, call.Name) }
	ctx := context.Background()
	run := func(body []byte, secret string, at time.Time) (int, string) {
		status, out := tools.Handle(ctx, body, SignWebhook(secret, body, at.Unix()))
		return status, string(out)
	}
	now := time.Now()
	cases := []struct {
		body   []byte
		status int
		want   string
	}{
		{toolCallBody("list_students", `{"cls":"JSS1"}`, nil), 200, `{"result":[{"asker":"u_7","cls":"JSS1","name":"Ada","school":"s1"}]}`},
		{toolCallBody("nothing", `null`, nil), 200, `{"result":"done"}`},
		{toolCallBody("denied", `{}`, nil), 200, `{"error":"You can only see your own classes."}`},
		{toolCallBody("boom", `{}`, nil), 200, `{"error":"The tool failed."}`},
		{toolCallBody("panics", `{}`, nil), 200, `{"error":"The tool failed."}`},
		{toolCallBody("missing", `{}`, nil), 200, `{"error":"There is no tool called missing."}`},
		{toolCallBody("constructor", `{}`, nil), 200, `{"error":"There is no tool called constructor."}`},
		{toolCallBody("list_students", `{}`, func(m map[string]any) { m["kind"] = "agent" }), 401, `{"error":"bad signature"}`},
		{toolCallBody("list_students", `{}`, func(m map[string]any) { m["type"] = "call.ended" }), 401, `{"error":"bad signature"}`},
		{[]byte("not json"), 401, `{"error":"bad signature"}`},
	}
	for _, c := range cases {
		status, out := run(c.body, "s", now)
		if status != c.status || out != c.want {
			t.Errorf("%s: got %d %s, want %d %s", c.body, status, out, c.status, c.want)
		}
	}
	if strings.Join(failures, ",") != "boom,panics" {
		t.Errorf("OnError saw %v", failures)
	}
	body := toolCallBody("list_students", `{"cls":"JSS1"}`, nil)
	if s, _ := run(body, "other", now); s != 401 {
		t.Errorf("wrong secret: %d", s)
	}
	if s, _ := run(body, "s", now.Add(-301*time.Second)); s != 401 {
		t.Errorf("stale: %d", s)
	}
	if s, _ := tools.Handle(ctx, body, ""); s != 401 {
		t.Errorf("no signature: %d", s)
	}

	// As an http.Handler.
	srv := httptest.NewServer(tools)
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(string(body)))
	req.Header.Set("X-SfuKit-Signature", SignWebhook("s", body, now.Unix()))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/json" || !strings.Contains(string(got), `"cls":"JSS1"`) {
		t.Errorf("ServeHTTP: %d %s", res.StatusCode, got)
	}
	res, err = http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", res.StatusCode)
	}
}

func TestAssistantAdmin(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.EscapedPath()+" "+r.Header.Get("Authorization")+" "+string(b))
		switch r.URL.Path {
		case "/assistant/config":
			if r.Method == http.MethodPut {
				w.Write([]byte(`{"config":` + string(b) + `}`))
			} else {
				w.Write([]byte(`{"config":{"name":"Ada","knowledge":true,"retentionDays":90}}`))
			}
		case "/assistant/tokens":
			w.Write([]byte(`{"token":"tok","conversationId":"c1","expiresAt":42}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k")
	ctx := context.Background()
	off := false
	cfg, err := c.SetAssistantConfig(ctx, AssistantConfig{
		Name:      "Ada",
		ToolsURL:  "https://app.example/assistant-tools",
		Tools:     []AssistantToolDefinition{{Name: "list_students", Description: "Students in a class", Parameters: map[string]any{"type": "object"}}},
		AppMap:    []AssistantAppPage{{Label: "Fees", Path: "/fees", Aliases: []string{"bursary"}}},
		Knowledge: &off,
	})
	if err != nil || cfg.Name != "Ada" || cfg.Knowledge == nil || *cfg.Knowledge {
		t.Fatalf("SetAssistantConfig: %+v %v", cfg, err)
	}
	want := `PUT /assistant/config Bearer k {"name":"Ada","tools":[{"name":"list_students","description":"Students in a class","parameters":{"type":"object"}}],"toolsUrl":"https://app.example/assistant-tools","appMap":[{"label":"Fees","path":"/fees","aliases":["bursary"]}],"knowledge":false}`
	if seen[0] != want {
		t.Errorf("got  %s\nwant %s", seen[0], want)
	}
	if cfg, err := c.AssistantConfig(ctx); err != nil || cfg.RetentionDays != 90 {
		t.Errorf("AssistantConfig: %+v %v", cfg, err)
	}
	tok, err := c.CreateAssistantToken(ctx, AssistantTokenRequest{UserID: "u_7", Name: "Ada", Role: "teacher", Metadata: map[string]string{"school": "s1"}, TTLSeconds: 600})
	if err != nil || tok.Token != "tok" || tok.ConversationID != "c1" || tok.ExpiresAt != 42 {
		t.Fatalf("CreateAssistantToken: %+v %v", tok, err)
	}
	if seen[2] != `POST /assistant/tokens Bearer k {"userId":"u_7","name":"Ada","role":"teacher","metadata":{"school":"s1"},"ttlSeconds":600}` {
		t.Errorf("token request: %s", seen[2])
	}
	if err := c.DeleteAssistantConversation(ctx, "u_7", "c1:x"); err != nil {
		t.Fatal(err)
	}
	if seen[3] != "DELETE /assistant/conversations/u_7/c1:x Bearer k " {
		t.Errorf("delete: %q", seen[3])
	}
	var apiErr *APIError
	if err := c.DeleteAssistantConversation(ctx, "u_7", ".."); !errors.As(err, &apiErr) || apiErr.Status != 400 || len(seen) != 4 {
		t.Errorf("dot segment: %v", err)
	}
}
