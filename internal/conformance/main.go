// Conformance smoke program for the Go SDK, driven by scripts/e2e-sdks.mjs.
// Usage (from go/): go run ./internal/conformance tokens|admin|webhook
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/appsotechltd/appsocom-go/sfukit"
)

func status(err error) int {
	var apiErr *sfukit.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	if err != nil {
		return -1
	}
	return 0
}

func must[T any](v T, err error) T {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return v
}

func main() {
	ctx := context.Background()
	env := os.Getenv
	kit := sfukit.NewClient(env("SFUKIT_URL"), env("SFUKIT_ADMIN_KEY"))
	room, user := env("ROOM"), env("USER_ID")
	out := map[string]any{}
	switch os.Args[1] {
	case "tokens":
		out["local"] = must(sfukit.SignToken(env("SFUKIT_JWT_SECRET"), sfukit.TokenRequest{RoomID: room, UserID: user, Name: "Ada", Role: "student", Metadata: map[string]any{"seat": 3}}))
		out["api"] = must(kit.CreateToken(ctx, sfukit.TokenRequest{RoomID: room, UserID: env("USER_ID2"), Role: "observer", Metadata: map[string]any{"via": "api"}}))
	case "admin":
		ids := []string{}
		for _, p := range must(kit.ListParticipants(ctx, room)) {
			ids = append(ids, p.UserID)
		}
		sort.Strings(ids)
		out["participants"] = ids
		out["metadataUpdated"] = must(kit.SetParticipantMetadata(ctx, room, user, map[string]any{"hand": "left"}))
		out["roleUpdated"] = must(kit.SetRole(ctx, room, user, "teacher", nil))
		must(0, kit.SetRoomMetadata(ctx, room, map[string]any{"topic": "sdk"}))
		out["roomMetadata"] = map[string]any{"topic": "sdk"} // Go returns no body here; the driver checks the room-updated message
		out["trackPermsUpdated"] = must(kit.SetTrackPermissions(ctx, room, user, &sfukit.TrackPermissions{AllowAll: false, Allow: []sfukit.TrackPermission{{UserID: "coach", Kinds: []string{"video"}}}}))
		out["muted"] = must(kit.Mute(ctx, room, user, []string{"mic"}, false))
		_, err := sfukit.NewClient(env("SFUKIT_URL"), "wrong").ListParticipants(ctx, room)
		out["badKeyStatus"] = status(err)
		_, err = kit.ListParticipants(ctx, "bad room!")
		out["badIdStatus"] = status(err)
		out["recording"] = must(kit.RecordingStatus(ctx, room))
		must(0, kit.Kick(ctx, room, user))
		out["removed"] = 1
	case "webhook":
		body := []byte(env("WH_BODY"))
		if ev, err := sfukit.VerifyWebhook(env("WH_SECRET"), body, env("WH_SIG")); err == nil {
			out["event"] = ev.ID
		} else {
			out["event"] = nil
		}
		if ev, _ := sfukit.VerifyWebhook(env("WH_SECRET"), append(body, ' '), env("WH_SIG")); ev != nil {
			out["tampered"] = ev.ID
		} else {
			out["tampered"] = nil
		}
		if ev, _ := sfukit.VerifyWebhook("not-the-secret", body, env("WH_SIG")); ev != nil {
			out["wrongSecret"] = ev.ID
		} else {
			out["wrongSecret"] = nil
		}
		t, _ := strconv.ParseInt(env("WH_T"), 10, 64)
		out["signature"] = sfukit.SignWebhook(env("WH_SECRET"), body, t)
	default:
		fmt.Fprintln(os.Stderr, "unknown mode")
		os.Exit(1)
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}
