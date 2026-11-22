package sfukit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAITask(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ai/complete" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "bad", 400)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(`{"task":"mark","tier":"standard","text":"{}","json":{"score":7,"feedback":"Good"},"stopReason":"end_turn","usage":{"model":"claude-haiku-5-5","credits":0.12}}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k")
	res, err := c.AITask(context.Background(), "mark", map[string]any{"question": "q", "answer": "a"}, &AIOptions{User: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if got["task"] != "mark" || got["user"] != "s1" || got["input"].(map[string]any)["answer"] != "a" {
		t.Fatalf("body %v", got)
	}
	var m struct {
		Score    float64 `json:"score"`
		Feedback string  `json:"feedback"`
	}
	if err := res.Decode(&m); err != nil || m.Score != 7 || res.Usage.Credits != 0.12 {
		t.Fatalf("decode %v %+v %+v", err, m, res.Usage)
	}
	if c.HTTPClient.Timeout != 10_000_000_000 {
		t.Fatal("the client's own timeout changed")
	}
}

func TestAICompleteError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"daily limit"}`))
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, "k").AIComplete(context.Background(), AICompleteRequest{Prompt: "x"})
	var apiErr *APIError
	if err == nil || !asAPIError(err, &apiErr) || apiErr.Status != 429 {
		t.Fatalf("err %v", err)
	}
}

func asAPIError(err error, out **APIError) bool {
	e, ok := err.(*APIError)
	if ok {
		*out = e
	}
	return ok
}

func TestAIResultDecodeEmpty(t *testing.T) {
	r := &AIResult{JSON: json.RawMessage("null"), Error: "cut off"}
	if err := r.Decode(&map[string]any{}); err == nil || err.Error() != "cut off" {
		t.Fatal(err)
	}
}
