package sfukit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTranslationAndInterpreters(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.URL.Path == "/languages":
			_, _ = w.Write([]byte(`{"languages":[{"code":"fr","name":"French","native":"Français","quality":"ready","text":true,"speechToText":true,"textToSpeech":true}]}`))
		case r.URL.Path == "/translate/text":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["from"] != "en" || in["tier"] != "smart" || in["context"] != nil {
				t.Errorf("translate body %v", in)
			}
			_, _ = w.Write([]byte(`{"from":"en","to":["fr"],"translations":{"fr":"Bonjour"},"text":"Bonjour","tier":"smart"}`))
		case r.URL.Path == "/translate/speech":
			b, _ := io.ReadAll(r.Body)
			if string(b) != "RIFF" || r.Header.Get("Content-Type") != "audio/wav" {
				t.Errorf("speech %q %q", b, r.Header.Get("Content-Type"))
			}
			_, _ = w.Write([]byte(`{"from":"en","transcript":"hello","seconds":2,"to":["fr"],"translations":{"fr":"bonjour"},"audio":{"fr":{"url":"https://x/a.mp3","expiresAt":9}}}`))
		case strings.HasSuffix(r.URL.Path, "/interpreters") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"interpreters":[{"agentId":"a1","language":"fr","listeners":2}],"max":3}`))
		case strings.HasSuffix(r.URL.Path, "/interpreters"):
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["language"] != "fr" || in["voiceName"] != "v2" {
				t.Errorf("interpreter body %v", in)
			}
			_, _ = w.Write([]byte(`{"roomId":"room-1","agentId":"a1"}`))
		default:
			_, _ = w.Write([]byte(`{"stopped":true}`))
		}
	}))
	defer srv.Close()
	kit := NewClient(srv.URL, "k")
	ctx := context.Background()
	langs, err := kit.Languages(ctx)
	if err != nil || len(langs) != 1 || langs[0].Native != "Français" || !langs[0].TextToSpeech {
		t.Fatalf("Languages = %+v, %v", langs, err)
	}
	tr, err := kit.TranslateText(ctx, TranslateTextRequest{Text: "Hello", To: []string{"fr"}, From: "en", Tier: "smart"})
	if err != nil || tr.Text != "Bonjour" || tr.Translations["fr"] != "Bonjour" {
		t.Fatalf("TranslateText = %+v, %v", tr, err)
	}
	sp, err := kit.TranslateSpeech(ctx, strings.NewReader("RIFF"), TranslateSpeechOptions{To: []string{"fr", "de"}, Speak: true, ContentType: "audio/wav"})
	if err != nil || sp.Transcript != "hello" || sp.Audio["fr"].URL != "https://x/a.mp3" {
		t.Fatalf("TranslateSpeech = %+v, %v", sp, err)
	}
	id, err := kit.StartInterpreter(ctx, "room-1", InterpreterRequest{Language: "fr", VoiceName: "v2"})
	if err != nil || id != "a1" {
		t.Fatalf("StartInterpreter = %q, %v", id, err)
	}
	list, err := kit.Interpreters(ctx, "room-1")
	if err != nil || len(list) != 1 || list[0].Listeners != 2 {
		t.Fatalf("Interpreters = %+v, %v", list, err)
	}
	if err := kit.StopInterpreter(ctx, "room-1", "a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := kit.StartInterpreter(ctx, "bad room", InterpreterRequest{Language: "fr"}); err == nil {
		t.Fatal("a bad room id is refused before sending")
	}
	want := []string{
		"GET /languages",
		"POST /translate/text",
		"POST /translate/speech?speak=true&to=fr%2Cde",
		"POST /rooms/room-1/interpreters",
		"GET /rooms/room-1/interpreters",
		"DELETE /rooms/room-1/interpreters/a1",
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s", strings.Join(seen, "\n"))
	}
}
