package sfukit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Language is one language from GET /languages: what each AI service can do in it.
type Language struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	Native       string `json:"native"`
	Quality      string `json:"quality"` // "ready" or "beta"
	Text         bool   `json:"text"`
	SpeechToText bool   `json:"speechToText"`
	TextToSpeech bool   `json:"textToSpeech"`
	RTL          bool   `json:"rtl,omitempty"`
}

// Languages lists the languages every AI service takes (public; good for a language picker).
func (c *Client) Languages(ctx context.Context) ([]Language, error) {
	var out struct {
		Languages []Language `json:"languages"`
	}
	if err := c.do(ctx, http.MethodGet, "/languages", nil, &out); err != nil {
		return nil, err
	}
	return out.Languages, nil
}

// TranslateTextRequest is POST /translate/text: up to 20,000 characters into up to 20 languages.
type TranslateTextRequest struct {
	Text      string            `json:"text"`
	To        []string          `json:"to"`
	From      string            `json:"from,omitempty"`      // unset = detected
	Tier      string            `json:"tier,omitempty"`      // "standard", "smart" or "best"
	Context   string            `json:"context,omitempty"`   // what the text is, for tone
	Formality string            `json:"formality,omitempty"` // "formal" or "informal"
	Glossary  map[string]string `json:"glossary,omitempty"`
	User      string            `json:"user,omitempty"`
	Label     string            `json:"label,omitempty"`
}

// Translation is what /translate/text and /translate/speech return.
type Translation struct {
	From         string            `json:"from"`
	To           []string          `json:"to"`
	Translations map[string]string `json:"translations"`
	Text         string            `json:"text,omitempty"` // when there is one target language
	Tier         string            `json:"tier,omitempty"`
	// Speech only.
	Transcript string                     `json:"transcript,omitempty"`
	Seconds    float64                    `json:"seconds,omitempty"`
	Audio      map[string]TranslatedAudio `json:"audio,omitempty"`
	Usage      map[string]any             `json:"usage,omitempty"`
}

// TranslatedAudio is a signed link to one spoken translation.
type TranslatedAudio struct {
	URL       string `json:"url"`
	ExpiresAt int64  `json:"expiresAt"`
}

// TranslateText translates text. Billed as AI Tasks credits, within the project's AI Tasks daily cap.
func (c *Client) TranslateText(ctx context.Context, req TranslateTextRequest) (*Translation, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var out Translation
	if err := c.do(ctx, http.MethodPost, "/translate/text", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TranslateSpeechOptions are the options of POST /translate/speech.
type TranslateSpeechOptions struct {
	To          []string
	From        string
	Speak       bool // voice the translations (up to 5 languages) as signed links
	Voice       string
	Format      string // "mp3" or "wav"
	Tier        string
	Context     string
	Formality   string
	User        string
	Label       string
	ContentType string // e.g. "audio/webm"; default application/octet-stream
}

// TranslateSpeech hears a recording (up to 25 MB), translates it and, with Speak, voices it.
// Errors from the server name the step that stopped ("speech to text", "translate", "text to speech").
func (c *Client) TranslateSpeech(ctx context.Context, audio io.Reader, opts TranslateSpeechOptions) (*Translation, error) {
	q := url.Values{}
	q.Set("to", strings.Join(opts.To, ","))
	for k, v := range map[string]string{"from": opts.From, "voice": opts.Voice, "format": opts.Format, "tier": opts.Tier, "context": opts.Context, "formality": opts.Formality, "user": opts.User, "label": opts.Label} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if opts.Speak {
		q.Set("speak", "true")
	}
	ct := opts.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	data, err := c.raw(ctx, "/translate/speech?"+q.Encode(), ct, audio, 1<<20)
	if err != nil {
		return nil, err
	}
	var out Translation
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// InterpreterRequest is POST /rooms/:roomId/interpreters.
type InterpreterRequest struct {
	Language  string         `json:"language"`
	From      string         `json:"from,omitempty"`
	Name      string         `json:"name,omitempty"`
	VoiceName string         `json:"voiceName,omitempty"`
	TTS       string         `json:"tts,omitempty"` // "cartesia" or "elevenlabs"
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Interpreter is one AI interpreter in a room.
type Interpreter struct {
	AgentID   string `json:"agentId"`
	Language  string `json:"language"`
	Listeners int    `json:"listeners"`
}

// StartInterpreter adds an AI interpreter to a room: it speaks what people say in req.Language.
// Only people who chose that language (room.setInterpretation, or the kits' My language menu)
// hear it. Up to 3 per room, one per language. Billed as AI Agent minutes plus Speech
// characters, within the project's Speech daily cap. Returns its agent id.
func (c *Client) StartInterpreter(ctx context.Context, roomID string, req InterpreterRequest) (string, error) {
	path, err := idPath(http.MethodPost, "/rooms/%s/interpreters", roomID)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	var out struct {
		AgentID string `json:"agentId"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return "", err
	}
	return out.AgentID, nil
}

// Interpreters lists a room's AI interpreters with how many people listen to each.
func (c *Client) Interpreters(ctx context.Context, roomID string) ([]Interpreter, error) {
	path, err := idPath(http.MethodGet, "/rooms/%s/interpreters", roomID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Interpreters []Interpreter `json:"interpreters"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Interpreters, nil
}

// StopInterpreter removes an AI interpreter from a room.
func (c *Client) StopInterpreter(ctx context.Context, roomID, agentID string) error {
	path, err := idPath(http.MethodDelete, "/rooms/%s/interpreters/%s", roomID, agentID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}
