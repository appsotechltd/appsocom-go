package sfukit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVideos(t *testing.T) {
	var seen []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		var b map[string]any
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &b)
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"videoId":"v","uploadUrl":"https://up","protocol":"put","videos":[{"videoId":"a","status":"ready"}],"next":"def","hls":"https://h"}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, AdminKey: "k"}
	ctx := context.Background()
	id := "0123456789abcdef0123456789abcdef"

	up, err := c.CreateVideoUpload(ctx, VideoUploadRequest{MaxDurationSeconds: 600, Name: "Lesson", UploadLength: 5000})
	if err != nil || up.UploadURL != "https://up" || up.Protocol != "put" {
		t.Fatalf("upload = %+v, %v", up, err)
	}
	videos, next, err := c.ListVideos(ctx, "abc", 10)
	if err != nil || len(videos) != 1 || next != "def" {
		t.Fatalf("list = %+v %q %v", videos, next, err)
	}
	if _, err := c.GetVideo(ctx, id); err != nil {
		t.Fatal(err)
	}
	name := "New"
	if _, err := c.UpdateVideo(ctx, id, VideoUpdate{Name: &name}); err != nil {
		t.Fatal(err)
	}
	p, err := c.VideoPlayback(ctx, id, 900)
	if err != nil || p.HLS != "https://h" {
		t.Fatalf("playback = %+v, %v", p, err)
	}
	if err := c.DeleteVideo(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetVideo(ctx, "../x"); err == nil {
		t.Fatal("bad id accepted")
	}
	want := []string{
		"POST /videos/uploads",
		"GET /videos?after=abc&limit=10",
		"GET /videos/" + id,
		"PATCH /videos/" + id,
		"POST /videos/" + id + "/playback",
		"DELETE /videos/" + id,
	}
	if len(seen) != len(want) {
		t.Fatalf("calls = %v", seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, seen[i], want[i])
		}
	}
	if bodies[0]["maxDurationSeconds"] != float64(600) || bodies[0]["uploadLength"] != float64(5000) {
		t.Fatalf("upload body = %v", bodies[0])
	}
	if bodies[4]["expiresInSeconds"] != float64(900) {
		t.Fatalf("playback body = %v", bodies[4])
	}
}
