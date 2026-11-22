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

func TestSpeech(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/speech/tts":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["response"] == "url" {
				if in["expiresIn"] != float64(600) {
					t.Errorf("expiresIn = %v", in["expiresIn"])
				}
				_, _ = w.Write([]byte(`{"url":"https://x/speech/audio/t/a.mp3?exp=1&sig=b","cached":true}`))
				return
			}
			if in["cache"] != false || in["language"] != "yo" {
				t.Errorf("body = %v", in)
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte{1, 2, 3})
		case "/speech/stt":
			if r.URL.Query().Get("language") != "ha" || r.Header.Get("Content-Type") != "audio/wav" {
				t.Errorf("stt request %s %s", r.URL, r.Header.Get("Content-Type"))
			}
			b, _ := io.ReadAll(r.Body)
			if string(b) != "RIFF" {
				t.Errorf("audio %q", b)
			}
			_, _ = w.Write([]byte(`{"text":"sannu","seconds":2,"provider":"spitch"}`))
		case "/speech/usage":
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"budget"}`))
		}
	}))
	defer srv.Close()
	kit := NewClient(srv.URL, "k")
	ctx := context.Background()
	audio, err := kit.TextToSpeech(ctx, TextToSpeechRequest{Text: "Ẹ káàárọ̀", Language: "yo", NoCache: true})
	if err != nil || len(audio) != 3 {
		t.Fatalf("TextToSpeech = %v, %v", audio, err)
	}
	link, err := kit.TextToSpeechURL(ctx, TextToSpeechRequest{Text: "hi"}, 10*time.Minute)
	if err != nil || !link.Cached || !strings.Contains(link.URL, "/speech/audio/") {
		t.Fatalf("TextToSpeechURL = %+v, %v", link, err)
	}
	out, err := kit.SpeechToText(ctx, strings.NewReader("RIFF"), SpeechToTextOptions{ContentType: "audio/wav", Language: "ha"})
	if err != nil || out.Text != "sannu" || out.Provider != "spitch" {
		t.Fatalf("SpeechToText = %+v, %v", out, err)
	}
	_, err = kit.SpeechUsage(ctx)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 429 {
		t.Fatalf("SpeechUsage err = %v", err)
	}
}
