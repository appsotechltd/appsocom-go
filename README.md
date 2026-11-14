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

Self-hosting your own deployment instead? Point the client at it and pass its admin key.

Docs: [docs.appsocom.dev](https://docs.appsocom.dev/server-sdks) · License: MIT
