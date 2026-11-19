# Appsocom Live Go SDK

Go server SDK for [Appsocom Live](https://live.appsocom.com): mint join tokens, manage rooms and participants, moderate, record, run live streams in and out, and verify webhooks. Standard library only, Go 1.22+.

```bash
go get github.com/appsotechltd/appsocom-go
```

```go
import (
	"context"
	"os"

	"github.com/appsotechltd/appsocom-go/sfukit"
)

kit := sfukit.NewClient("https://api.appsocom.dev", os.Getenv("APPSOCOM_API_KEY")) // project API key from the console
tok, err := kit.CreateToken(context.Background(), sfukit.TokenRequest{RoomID: "class-1", UserID: "u_7", Name: "Ada", Role: "student"})
```

Errors are `*sfukit.APIError` with the HTTP status and body. Verify webhooks with `sfukit.VerifyWebhook(secret, rawBody, r.Header.Get("X-SfuKit-Signature"))`.

## AI assistant

Tell the assistant about your app once, mint a token for each signed-in person, and answer its tool calls at your `toolsUrl`:

```go
_, err = kit.SetAssistantConfig(ctx, sfukit.AssistantConfig{
	Name:     "Ada",
	ToolsURL: "https://app.example/assistant-tools",
	Tools: []sfukit.AssistantToolDefinition{{
		Name: "list_students", Description: "Students in a class",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"cls": map[string]any{"type": "string"}}},
	}},
	AppMap: []sfukit.AssistantAppPage{{Label: "Fees", Path: "/fees", Aliases: []string{"bursary"}}},
})
tok, err := kit.CreateAssistantToken(ctx, sfukit.AssistantTokenRequest{UserID: user.ID, Name: user.Name, Role: user.Role, Metadata: map[string]string{"school": user.SchoolID}})
// give tok.Token to the browser (@appsocom/react <AssistantPanel>)

tools := sfukit.NewAssistantTools(os.Getenv("APPSOCOM_WEBHOOK_SECRET"), map[string]sfukit.AssistantToolHandler{
	"list_students": func(ctx context.Context, args json.RawMessage, call *sfukit.AssistantToolCall) (any, error) {
		if call.Role != "teacher" {
			return nil, sfukit.NewAssistantToolError("Only teachers can list students.") // the model sees this message
		}
		var in struct{ Cls string `json:"cls"` }
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, err
		}
		return db.Students(ctx, call.Metadata["school"], in.Cls)
	},
})
tools.OnError = func(err error, call *sfukit.AssistantToolCall) { log.Printf("tool %s: %v", call.Name, err) }
http.Handle("/assistant-tools", tools)
```

`AssistantTools` is an `http.Handler` (or call `Handle(ctx, rawBody, signature)` yourself). It refuses bad or stale signatures with 401, answers `{"result": ...}` (`"done"` when a tool returns nil) or `{"error": ...}`, and hides any error other than an `*AssistantToolError`, and any panic, as "The tool failed.". Also: `AssistantConfig`, `DeleteAssistantConversation`, `VerifyAssistantToolCall`.

Self-hosting your own deployment instead? Point the client at it and pass its admin key.

Docs: [docs.appsocom.dev](https://docs.appsocom.dev/server-sdks) · License: MIT
