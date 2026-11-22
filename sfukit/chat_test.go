package sfukit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatCalls(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/chat/tokens":
			w.Write([]byte(`{"token":"tok","expiresAt":1}`))
		case r.URL.Path == "/chat/conversations" && r.Method == http.MethodPost:
			w.Write([]byte(`{"conversation":{"id":"grp_1","type":"group","title":"T","status":"open","assignee":null,"metadata":{},"createdAt":1,"updatedAt":1,"lastSeq":0},"members":[{"userId":"u1","name":"Ada","role":"member","joinedAt":1,"lastReadSeq":0}]}`))
		case r.URL.Path == "/chat/conversations":
			w.Write([]byte(`{"conversations":[{"conversation":{"id":"grp_1","assignee":null,"metadata":{}},"last":null,"unread":2,"memberCount":1}]}`))
		case r.URL.Path == "/chat/email" && r.Method == http.MethodPut:
			w.Write([]byte(`{"email":{"address":"help@s.test","gatewayUrl":"https://mail.api.appsocom.dev","enabled":true,"lastCheckedAt":null,"lastError":null,"received":0}}`))
		case r.URL.Path == "/chat/email":
			w.Write([]byte(`{"email":null}`))
		case r.URL.Path == "/chat/tickets/T-1001":
			w.Write([]byte(`{"conversation":{"id":"sup_1","type":"support","number":1001,"priority":"high","assignee":"ag1","metadata":{}},"members":[]}`))
		default:
			w.Write([]byte(`{"message":{"id":"m1","seq":3,"kind":"system","text":"hi","attachments":[]},"conversation":{"id":"sup_1","assignee":"ag1","metadata":{}}}`))
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "key")
	ctx := context.Background()
	em, err := c.SetChatEmail(ctx, ChatEmail{Address: "help@s.test", Password: "pw", Name: "School"})
	if err != nil || em.Address != "help@s.test" || !em.Enabled {
		t.Fatal(em, err)
	}
	if off, err := c.ChatEmail(ctx); err != nil || off != nil {
		t.Fatal(off, err)
	}
	seen = nil
	tok, err := c.CreateChatToken(ctx, ChatTokenRequest{UserID: "u1", Role: "agent"})
	if err != nil || tok.Token != "tok" {
		t.Fatal(tok, err)
	}
	info, err := c.CreateChatConversation(ctx, CreateChatConversation{Type: "group", Title: "T", Members: []ChatMemberInput{{UserID: "u1", Name: "Ada"}}})
	if err != nil || info.Conversation.ID != "grp_1" || len(info.Members) != 1 {
		t.Fatal(info, err)
	}
	list, err := c.ListChatConversations(ctx, ChatConversationFilter{UserID: "u1", Priority: "high"})
	if err != nil || len(list) != 1 || list[0].Unread != 2 {
		t.Fatal(list, err)
	}
	tk, err := c.Ticket(ctx, "T-1001")
	if err != nil || tk.Conversation.Number != 1001 || tk.Conversation.Assignee != "ag1" {
		t.Fatal(tk, err)
	}
	m, err := c.PostChatMessage(ctx, "sup_1", ChatPost{Text: "hi"})
	if err != nil || m.Seq != 3 {
		t.Fatal(m, err)
	}
	if _, err := c.ClaimTicket(ctx, "sup_1", "ag1", "Grace"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ChatConversation(ctx, ".."); err == nil {
		t.Fatal("a bad id was sent")
	}
	want := []string{"POST /chat/tokens", "POST /chat/conversations", "GET /chat/conversations?priority=high&userId=u1", "GET /chat/tickets/T-1001", "POST /chat/conversations/sup_1/messages", "POST /chat/conversations/sup_1/claim"}
	for i, w := range want {
		if len(seen) <= i || seen[i][:len(w)] != w {
			t.Fatalf("call %d = %q, want %q", i, seen, w)
		}
	}
	var claim map[string]string
	_ = json.Unmarshal([]byte(seen[5][len(want[5])+1:]), &claim)
	if claim["userId"] != "ag1" || claim["name"] != "Grace" {
		t.Fatal(seen[5])
	}
}
