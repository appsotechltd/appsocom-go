package sfukit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Prints a token for the cross-language check when SFUKIT_TEST_SECRET is set.
func TestSignToken(t *testing.T) {
	secret := os.Getenv("SFUKIT_TEST_SECRET")
	if secret == "" {
		secret = "test-secret-test-secret-test-secret-1234"
	}
	tok, err := SignToken(secret, TokenRequest{
		RoomID: "go-room", UserID: "go-user", Name: "Gopher",
		Role: "student", Permissions: &PermissionsPatch{Screen: Bool(true)},
		Metadata: map[string]any{"team": "blue"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SignToken("short", TokenRequest{RoomID: "r", UserID: "u"}); err == nil {
		t.Fatal("expected short secret error")
	}
	if _, err := SignToken(secret, TokenRequest{RoomID: "bad room", UserID: "u"}); err == nil {
		t.Fatal("expected invalid room error")
	}
	claims := decode(t, tok.Token)
	if claims["role"] != "student" || claims["pub"] != true {
		t.Fatalf("role/pub claims: %v", claims)
	}
	if md, _ := claims["md"].(map[string]any); md["team"] != "blue" {
		t.Fatalf("md claim: %v", claims["md"])
	}
	if _, err := SignToken(secret, TokenRequest{RoomID: "r", UserID: "u", Metadata: map[string]any{"x": strings.Repeat("a", 5000)}}); err == nil {
		t.Fatal("expected metadata too large error")
	}
	perm, _ := claims["perm"].(map[string]any)
	if perm["mic"] != true || perm["camera"] != true || perm["screen"] != true || perm["moderate"] != false {
		t.Fatalf("perm claim: %v", perm)
	}
	obs, _ := SignToken(secret, TokenRequest{RoomID: "r", UserID: "u", Role: "observer"})
	if c := decode(t, obs.Token); c["pub"] != false {
		t.Fatalf("observer should not publish: %v", c)
	}
	legacy, _ := SignToken(secret, TokenRequest{RoomID: "r", UserID: "u", CanPublish: Bool(false)})
	if c := decode(t, legacy.Token); c["pub"] != false || c["role"] != nil {
		t.Fatalf("canPublish=false without role: %v", c)
	}
	if _, err := SignToken(secret, TokenRequest{RoomID: "r", UserID: "u", Role: "bad role"}); err == nil {
		t.Fatal("expected invalid role error")
	}
	enc, _ := SignToken(secret, TokenRequest{RoomID: "r", UserID: "u", E2EE: true})
	if c := decode(t, enc.Token); c["e2ee"] != true {
		t.Fatalf("e2ee claim missing: %v", c)
	}
	if c := decode(t, legacy.Token); c["e2ee"] != nil {
		t.Fatalf("e2ee claim should be absent by default: %v", c)
	}
	if p := os.Getenv("SFUKIT_TOKEN_OUT"); p != "" {
		_ = os.WriteFile(p, []byte(tok.Token), 0o600)
	}
}

func decode(t *testing.T, tok string) map[string]any {
	t.Helper()
	parts := strings.Split(tok, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestVerifyWebhook(t *testing.T) {
	body := []byte(`{"id":"e1","event":"room_started","roomId":"r","createdAt":1,"seq":1}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(ts + "." + string(body)))
	header := "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
	ev, err := VerifyWebhook("s3cret", body, header)
	if err != nil || ev.Event != "room_started" || ev.Seq != 1 {
		t.Fatalf("verify: %v %v", ev, err)
	}
	if _, err := VerifyWebhook("other", body, header); err == nil {
		t.Fatal("expected bad signature")
	}
	if _, err := VerifyWebhook("s3cret", body, "t=1000,v1=00"); err == nil {
		t.Fatal("expected too old")
	}
}

func TestRecordingClient(t *testing.T) {
	var calls []string
	var startBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			startBody = string(b)
		}
		if r.Header.Get("Authorization") != "Bearer key" {
			w.WriteHeader(401)
			return
		}
		switch r.Method {
		case http.MethodPost, http.MethodGet:
			_, _ = w.Write([]byte(`{"roomId":"r1","recording":{"id":"rec1","startedAt":1,"startedBy":"admin"}}`))
		case http.MethodDelete:
			_, _ = w.Write([]byte(`{"roomId":"r1","recordingId":"rec1","stopReason":"admin","files":[{"key":"rooms/r1/recordings/rec1/001-u-audio.wav","userId":"u","kind":"audio","durationMs":1500,"bytes":144044}],"errors":[],"manifestKey":"rooms/r1/recordings/rec1/manifest.json"}`))
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "key")
	ctx := context.Background()
	rec, err := c.StartRecording(ctx, "r1", RecordingOptions{Video: true})
	if err != nil || rec.ID != "rec1" || startBody != `{"video":true}` {
		t.Fatalf("start: %v %v %s", rec, err, startBody)
	}
	if rec, err = c.RecordingStatus(ctx, "r1"); err != nil || rec.StartedBy != "admin" {
		t.Fatalf("status: %v %v", rec, err)
	}
	m, err := c.StopRecording(ctx, "r1")
	if err != nil || len(m.Files) != 1 || m.Files[0].DurationMs != 1500 || m.ManifestKey == "" {
		t.Fatalf("stop: %+v %v", m, err)
	}
	want := "POST /rooms/r1/recording,GET /rooms/r1/recording,DELETE /rooms/r1/recording"
	if got := strings.Join(calls, ","); got != want {
		t.Fatalf("calls %s", got)
	}
}
func TestIngressClient(t *testing.T) {
	var gotPath, gotMethod, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if r.Header.Get("Authorization") != "Bearer admin" {
			w.WriteHeader(401)
			return
		}
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(201)
			io.WriteString(w, `{"ingressId":"in_x","roomId":"r","name":"Cam","userId":"cam","whipUrl":"https://k/whip/r/in_x","streamKey":"sk_1","createdAt":1,"live":false}`)
		case http.MethodGet:
			io.WriteString(w, `{"roomId":"r","ingress":[{"ingressId":"in_x","live":true,"liveSince":5}]}`)
		default:
			io.WriteString(w, `{}`)
		}
	}))
	defer srv.Close()
	kit := NewClient(srv.URL, "admin")
	ctx := context.Background()

	in, err := kit.CreateIngress(ctx, "r", IngressRequest{Name: "Cam", UserID: "cam"})
	if err != nil || in.StreamKey != "sk_1" || in.WHIPURL != "https://k/whip/r/in_x" {
		t.Fatalf("create: %v %+v", err, in)
	}
	if gotPath != "/rooms/r/ingress" || gotMethod != "POST" || gotBody != `{"name":"Cam","userId":"cam"}` {
		t.Fatalf("create request: %s %s %s", gotMethod, gotPath, gotBody)
	}
	list, err := kit.ListIngress(ctx, "r")
	if err != nil || len(list) != 1 || !list[0].Live || list[0].LiveSince != 5 {
		t.Fatalf("list: %v %+v", err, list)
	}
	if err := kit.DeleteIngress(ctx, "r", "in_x"); err != nil || gotPath != "/rooms/r/ingress/in_x" || gotMethod != "DELETE" {
		t.Fatalf("delete: %v %s %s", err, gotMethod, gotPath)
	}
}

func TestRTMPIngressClient(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(201)
		io.WriteString(w, `{"ingressId":"in_y","roomId":"r","protocol":"rtmp","name":"Studio","userId":"ingress-in_y","streamInputId":"abc","encoderStatus":null,"createdAt":1,"live":false,`+
			`"rtmps":{"url":"rtmps://live.cloudflare.com:443/live/","streamKey":"k1"},"srt":{"url":"srt://live.cloudflare.com:778","streamId":"s1","passphrase":"p1"}}`)
	}))
	defer srv.Close()
	in, err := NewClient(srv.URL, "admin").CreateIngress(context.Background(), "r", IngressRequest{Name: "Studio", Protocol: "rtmp"})
	if err != nil || in.Protocol != "rtmp" || in.RTMPS == nil || in.RTMPS.StreamKey != "k1" || in.SRT == nil || in.SRT.Passphrase != "p1" || in.StreamInputID != "abc" || in.WHIPURL != "" {
		t.Fatalf("create rtmp: %v %+v", err, in)
	}
	if gotBody != `{"name":"Studio","protocol":"rtmp"}` {
		t.Fatalf("body: %s", gotBody)
	}
}

func TestEgressClient(t *testing.T) {
	var gotPath, gotMethod, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/destinations"):
			io.WriteString(w, `{"id":"out9","url":"rtmps://a.rtmps.youtube.com/live2"}`)
		case r.Method == http.MethodPost:
			w.WriteHeader(201)
			io.WriteString(w, `{"egressId":"eg_1","roomId":"r","status":"starting","via":"cloudflare-stream","playback":{"hls":"https://x/m.m3u8"},"destinations":[{"id":"out1","url":"rtmps://a.rtmps.youtube.com/live2"}]}`)
		case r.Method == http.MethodGet:
			io.WriteString(w, `{"egress":[{"egressId":"eg_1","status":"live"}]}`)
		default:
			io.WriteString(w, `{"egressId":"eg_1","status":"stopped"}`)
		}
	}))
	defer srv.Close()
	kit := NewClient(srv.URL, "admin")
	ctx := context.Background()
	eg, err := kit.StartEgress(ctx, "r", EgressRequest{Destinations: []EgressDestination{{Platform: "youtube", StreamKey: "k"}}})
	if err != nil || eg.Playback["hls"] != "https://x/m.m3u8" || eg.Destinations[0].ID != "out1" {
		t.Fatalf("start: %v %+v", err, eg)
	}
	if gotPath != "/rooms/r/egress" || gotBody != `{"destinations":[{"platform":"youtube","streamKey":"k"}]}` {
		t.Fatalf("start request: %s %s", gotPath, gotBody)
	}
	list, err := kit.ListEgress(ctx, "r")
	if err != nil || len(list) != 1 || list[0].Status != "live" {
		t.Fatalf("list: %v %+v", err, list)
	}
	d, err := kit.AddEgressDestination(ctx, "r", "eg_1", EgressDestination{Platform: "youtube", StreamKey: "k2"})
	if err != nil || d.ID != "out9" || gotPath != "/rooms/r/egress/eg_1/destinations" {
		t.Fatalf("add dest: %v %+v %s", err, d, gotPath)
	}
	if err := kit.RemoveEgressDestination(ctx, "r", "eg_1", "out9"); err != nil || gotMethod != "DELETE" || gotPath != "/rooms/r/egress/eg_1/destinations/out9" {
		t.Fatalf("remove dest: %v %s %s", err, gotMethod, gotPath)
	}
	st, err := kit.StopEgress(ctx, "r", "eg_1")
	if err != nil || st.Status != "stopped" || gotPath != "/rooms/r/egress/eg_1" {
		t.Fatalf("stop: %v %+v", err, st)
	}
}

func TestUsage(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.RequestURI(), r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"rows":[{"key":"2026-11-12","event":"participant","count":3,"seconds":600,"minutes":10,"bytes":0}]}`))
	}))
	defer srv.Close()
	kit := NewClient(srv.URL, "sfk_admin")
	rows, err := kit.Usage(context.Background(), UsageQuery{Days: 30, GroupBy: "room", Customer: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sfk_admin" || !strings.HasPrefix(gotPath, "/usage?") || !strings.Contains(gotPath, "days=30") || !strings.Contains(gotPath, "groupBy=room") || !strings.Contains(gotPath, "customer=acme") {
		t.Fatalf("request: %s %s", gotPath, gotAuth)
	}
	if len(rows) != 1 || rows[0].Minutes != 10 || rows[0].Count != 3 {
		t.Fatalf("rows: %+v", rows)
	}
}

func TestSignWebhookRoundTrip(t *testing.T) {
	body := []byte(`{"id":"evt_9","event":"room_started"}`)
	h := SignWebhook("s", body, time.Now().Unix())
	ev, err := VerifyWebhook("s", body, h)
	if err != nil || ev.ID != "evt_9" {
		t.Fatalf("round trip: %v %v", ev, err)
	}
}

func TestAPIErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, "wrong").ListParticipants(context.Background(), "r1")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 {
		t.Fatalf("want APIError 401, got %v", err)
	}
}

func TestPhonePins(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		switch r.Method {
		case http.MethodPost:
			_, _ = w.Write([]byte(`{"pin":"12345678","roomId":"r1","expiresAt":1,"label":"Dr Ade"}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"pins":[{"pin":"12345678","roomId":"r1","expiresAt":1}]}`))
		default:
			_, _ = w.Write([]byte(`{"deleted":true}`))
		}
	}))
	defer srv.Close()
	kit := NewClient(srv.URL, "sfk_admin")
	pin, err := kit.CreatePhonePin(context.Background(), PhonePinRequest{RoomID: "r1", Label: "Dr Ade"})
	if err != nil || pin.Pin != "12345678" || pin.Label != "Dr Ade" {
		t.Fatalf("create: %+v %v", pin, err)
	}
	pins, err := kit.PhonePins(context.Background(), "r1")
	if err != nil || len(pins) != 1 {
		t.Fatalf("list: %+v %v", pins, err)
	}
	if err := kit.DeletePhonePin(context.Background(), "12345678"); err != nil {
		t.Fatal(err)
	}
	want := []string{`POST /phone/pins {"roomId":"r1","label":"Dr Ade"}`, "GET /phone/pins?roomId=r1 ", "DELETE /phone/pins/12345678 "}
	for i, w := range want {
		if calls[i] != w {
			t.Errorf("call %d: %q, want %q", i, calls[i], w)
		}
	}
}

func TestTenantKeysAndWebhook(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(b))
		switch {
		case r.URL.Path == "/tenants/acme/keys" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"key":{"id":"key_1","name":"live","prefix":"sfk_t_acme_ab","createdAt":1,"lastUsedAt":null},"apiKey":"sfk_t_acme_secret"}`))
		case r.URL.Path == "/keys" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"keys":[{"id":"default","name":"default","prefix":"sfk_t_acme_cd","createdAt":1,"lastUsedAt":5}]}`))
		case r.URL.Path == "/tenants/acme/webhook":
			_, _ = w.Write([]byte(`{"url":"https://acme.test/hook","secret":"whsec_x"}`))
		case r.URL.Path == "/webhook/rotate-secret":
			_, _ = w.Write([]byte(`{"secret":"whsec_y"}`))
		case r.URL.Path == "/tenants/acme/webhook/test":
			_, _ = w.Write([]byte(`{"delivered":true,"status":200}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "key")
	ctx := context.Background()
	k, secret, err := c.CreateTenantKey(ctx, "acme", "live")
	if err != nil || k.ID != "key_1" || secret != "sfk_t_acme_secret" || k.LastUsedAt != nil {
		t.Fatalf("create key: %+v %q %v", k, secret, err)
	}
	keys, err := c.ListTenantKeys(ctx, "")
	if err != nil || len(keys) != 1 || keys[0].LastUsedAt == nil || *keys[0].LastUsedAt != 5 {
		t.Fatalf("list keys: %+v %v", keys, err)
	}
	if err := c.DeleteTenantKey(ctx, "acme", "key_1"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetTenantWebhook(ctx, "acme", "https://acme.test/hook"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetTenantWebhook(ctx, "", ""); err != nil {
		t.Fatal(err)
	}
	hook, err := c.TenantWebhook(ctx, "acme")
	if err != nil || hook.URL != "https://acme.test/hook" || hook.Secret != "whsec_x" {
		t.Fatalf("webhook: %+v %v", hook, err)
	}
	if s, err := c.RotateTenantWebhookSecret(ctx, ""); err != nil || s != "whsec_y" {
		t.Fatalf("rotate: %q %v", s, err)
	}
	if r, err := c.TestTenantWebhook(ctx, "acme"); err != nil || !r.Delivered || r.Status != 200 {
		t.Fatalf("test: %+v %v", r, err)
	}
	want := []string{
		`POST /tenants/acme/keys {"name":"live"}`,
		`GET /keys `,
		`DELETE /tenants/acme/keys/key_1 `,
		`PATCH /tenants/acme {"webhookUrl":"https://acme.test/hook"}`,
		`PUT /webhook {"url":null}`,
		`GET /tenants/acme/webhook `,
		`POST /webhook/rotate-secret `,
		`POST /tenants/acme/webhook/test `,
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestTenantsUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usage/tenants" || r.URL.Query().Get("period") != "2026-11" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"period":"2026-11","tenants":[{"tenant":"acme","period":"2026-11","participantMinutes":12.5,"agentMinutes":3,"videoRecordingMinutes":1.5}]}`))
	}))
	defer srv.Close()
	got, err := NewClient(srv.URL, "key").TenantsUsage(context.Background(), "2026-11")
	if err != nil || len(got) != 1 || got[0].Tenant != "acme" || got[0].ParticipantMinutes != 12.5 || got[0].AgentMinutes != 3 || got[0].VideoRecordingMinutes != 1.5 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestSignTokenDataSaver(t *testing.T) {
	secret := "test-secret-test-secret-test-secret-1234"
	tok, err := SignToken(secret, TokenRequest{RoomID: "r", UserID: "u", DataSaver: "minimal"})
	if err != nil {
		t.Fatal(err)
	}
	if c := decode(t, tok.Token); c["ds"] != "minimal" {
		t.Fatalf("ds claim: %v", c)
	}
	off, _ := SignToken(secret, TokenRequest{RoomID: "r", UserID: "u", DataSaver: "off"})
	if c := decode(t, off.Token); c["ds"] != nil {
		t.Fatalf("off should add no claim: %v", c)
	}
	if _, err := SignToken(secret, TokenRequest{RoomID: "r", UserID: "u", DataSaver: "ultra"}); err == nil {
		t.Fatal("expected bad dataSaver error")
	}
}
