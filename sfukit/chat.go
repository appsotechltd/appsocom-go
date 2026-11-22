package sfukit

// Appsocom Chat and Ticket Support, from your Go server.
//
//	// Once: settings (the AI answers support chats first, retention…).
//	ai := true
//	client.SetChatConfig(ctx, sfukit.ChatConfig{Support: &sfukit.ChatSupportConfig{AI: &ai, AIName: "Ada"}})
//
//	// Who may talk to whom is your app's call: make conversations here.
//	conv, _ := client.CreateChatConversation(ctx, sfukit.CreateChatConversation{
//	    Type: "group", Title: "JSS1 parents",
//	    Members: []sfukit.ChatMemberInput{{UserID: "u1", Name: "Ada"}, {UserID: "u2", Name: "Bayo"}},
//	})
//
//	// Each person gets a chat token for their browser or app ("agent" for support staff).
//	tok, _ := client.CreateChatToken(ctx, sfukit.ChatTokenRequest{UserID: user.ID, Name: user.Name})

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// ChatTokenRequest says who a chat token is for.
type ChatTokenRequest struct {
	UserID string `json:"userId"`
	Name   string `json:"name,omitempty"`
	// Role is "member" (default) or "agent" (also answers the support queue).
	Role string `json:"role,omitempty"`
	// Knowledge: support chats this person opens answer from this Knowledge collection.
	Knowledge string `json:"knowledge,omitempty"`
	// Metadata is kept on support chats this person opens (at most 2 KB).
	Metadata   map[string]string `json:"metadata,omitempty"`
	TTLSeconds int               `json:"ttlSeconds,omitempty"`
}

// ChatToken is a signed chat token and its expiry (Unix seconds).
type ChatToken struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"`
}

// ChatSupportConfig is how support chats (tickets) work.
type ChatSupportConfig struct {
	// AI: the AI Assistant answers first and hands over to people.
	AI            *bool  `json:"ai,omitempty"`
	AIName        string `json:"aiName,omitempty"`
	Tier          string `json:"tier,omitempty"`
	AITools       *bool  `json:"aiTools,omitempty"`
	Knowledge     string `json:"knowledge,omitempty"`
	Greeting      string `json:"greeting,omitempty"`
	HandOverLabel string `json:"handOverLabel,omitempty"`
	Title         string `json:"title,omitempty"`
}

// ChatConfig is a project's chat settings. SetChatConfig replaces them all.
type ChatConfig struct {
	RetentionDays int     `json:"retentionDays,omitempty"`
	MaxFileMB     float64 `json:"maxFileMB,omitempty"`
	DailyMessages int     `json:"dailyMessages,omitempty"`
	// MessageWebhooks: "offline" (default), "all" or "none".
	MessageWebhooks string             `json:"messageWebhooks,omitempty"`
	Support         *ChatSupportConfig `json:"support,omitempty"`
}

// ChatMemberInput is a member to add.
type ChatMemberInput struct {
	UserID string `json:"userId"`
	Name   string `json:"name,omitempty"`
	// Role is "member" (default), "agent" or "owner".
	Role string `json:"role,omitempty"`
}

// ChatMember is a member as stored.
type ChatMember struct {
	UserID      string `json:"userId"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	JoinedAt    int64  `json:"joinedAt"`
	LastReadSeq int64  `json:"lastReadSeq"`
}

// ChatAttachment is a file on a message.
type ChatAttachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// ChatMessage is one message.
type ChatMessage struct {
	ID          string            `json:"id"`
	Seq         int64             `json:"seq"`
	UserID      string            `json:"userId"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Text        string            `json:"text"`
	Attachments []ChatAttachment  `json:"attachments"`
	ReplyTo     string            `json:"replyTo,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   int64             `json:"createdAt"`
	EditedAt    int64             `json:"editedAt,omitempty"`
	Deleted     bool              `json:"deleted,omitempty"`
}

// ChatConversation is a direct chat, a group or a support chat (a ticket).
type ChatConversation struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status string `json:"status"`
	// Assignee is the agent who has a support chat ("" = nobody).
	Assignee  string   `json:"assignee"`
	Number    int64    `json:"number,omitempty"`
	Priority  string   `json:"priority,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Channel   string   `json:"channel,omitempty"`
	Knowledge string   `json:"knowledge,omitempty"`
	// Email is set on email tickets: who wrote in and the email thread.
	Email     *ChatTicketEmail  `json:"email,omitempty"`
	Metadata  map[string]string `json:"metadata"`
	CreatedAt int64             `json:"createdAt"`
	UpdatedAt int64             `json:"updatedAt"`
	LastSeq   int64             `json:"lastSeq"`
}

// ChatConversationInfo is a conversation with its members.
type ChatConversationInfo struct {
	Conversation ChatConversation `json:"conversation"`
	Members      []ChatMember     `json:"members"`
	Existing     bool             `json:"existing,omitempty"`
}

// ChatInboxEntry is one line of a list of conversations.
type ChatInboxEntry struct {
	Conversation ChatConversation `json:"conversation"`
	Last         *struct {
		UserID string `json:"userId"`
		Name   string `json:"name"`
		Text   string `json:"text"`
		At     int64  `json:"at"`
	} `json:"last"`
	Unread      int    `json:"unread,omitempty"`
	Role        string `json:"role,omitempty"`
	MemberCount int    `json:"memberCount"`
}

// CreateChatConversation is what CreateChatConversation makes.
type CreateChatConversation struct {
	// Type is "direct" (exactly two members), "group" or "support".
	Type     string            `json:"type"`
	ID       string            `json:"id,omitempty"`
	Title    string            `json:"title,omitempty"`
	Members  []ChatMemberInput `json:"members"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Priority string            `json:"priority,omitempty"`
	Tags     []string          `json:"tags,omitempty"`
}

// ChatConversationFilter narrows ListChatConversations.
type ChatConversationFilter struct {
	UserID   string
	Type     string
	Status   string
	Assignee string
	Priority string
	Before   int64
	Limit    int
}

// ChatPost is a message posted by your backend: a system note (no UserID) or as a member.
type ChatPost struct {
	Text        string            `json:"text,omitempty"`
	UserID      string            `json:"userId,omitempty"`
	Name        string            `json:"name,omitempty"`
	Kind        string            `json:"kind,omitempty"`
	Attachments []string          `json:"attachments,omitempty"`
	ReplyTo     string            `json:"replyTo,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// ChatConversationPatch changes a conversation; nil fields stay as they are.
type ChatConversationPatch struct {
	Title    *string `json:"title,omitempty"`
	Status   *string `json:"status,omitempty"`
	Assignee *string `json:"assignee,omitempty"`
	// AssigneeName names a new assignee, who joins as an agent.
	AssigneeName string            `json:"assigneeName,omitempty"`
	Priority     string            `json:"priority,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// ChatHistoryEntry is one change to a conversation.
type ChatHistoryEntry struct {
	At     int64           `json:"at"`
	By     string          `json:"by"`
	Change string          `json:"change"`
	From   json.RawMessage `json:"from,omitempty"`
	To     json.RawMessage `json:"to,omitempty"`
}

func (c *Client) chatJSON(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = b
	}
	return c.do(ctx, method, path, body, out)
}

// CreateChatToken mints a chat token for one person's browser or app.
func (c *Client) CreateChatToken(ctx context.Context, req ChatTokenRequest) (*ChatToken, error) {
	var out ChatToken
	if err := c.chatJSON(ctx, http.MethodPost, "/chat/tokens", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetChatConfig replaces the project's chat settings.
func (c *Client) SetChatConfig(ctx context.Context, cfg ChatConfig) (*ChatConfig, error) {
	var out struct {
		Config ChatConfig `json:"config"`
	}
	if err := c.chatJSON(ctx, http.MethodPut, "/chat/config", cfg, &out); err != nil {
		return nil, err
	}
	return &out.Config, nil
}

// CreateChatConversation makes a conversation. A direct chat between the same
// two people returns the existing one (Existing true).
func (c *Client) CreateChatConversation(ctx context.Context, in CreateChatConversation) (*ChatConversationInfo, error) {
	var out ChatConversationInfo
	if err := c.chatJSON(ctx, http.MethodPost, "/chat/conversations", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListChatConversations lists conversations, newest activity first (with UserID: theirs, with unread counts).
func (c *Client) ListChatConversations(ctx context.Context, f ChatConversationFilter) ([]ChatInboxEntry, error) {
	q := url.Values{}
	for k, v := range map[string]string{"userId": f.UserID, "type": f.Type, "status": f.Status, "assignee": f.Assignee, "priority": f.Priority} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if f.Before > 0 {
		q.Set("before", strconv.FormatInt(f.Before, 10))
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	path := "/chat/conversations"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Conversations []ChatInboxEntry `json:"conversations"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Conversations, nil
}

// ChatConversation returns a conversation and its members.
func (c *Client) ChatConversation(ctx context.Context, conversationID string) (*ChatConversationInfo, error) {
	path, err := idPath(http.MethodGet, "/chat/conversations/%s", conversationID)
	if err != nil {
		return nil, err
	}
	var out ChatConversationInfo
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Ticket returns a support chat by its number (1001, or "T-1001").
func (c *Client) Ticket(ctx context.Context, number string) (*ChatConversationInfo, error) {
	path, err := segPath(http.MethodGet, "/chat/tickets/%s", number)
	if err != nil {
		return nil, err
	}
	var out ChatConversationInfo
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateChatConversation changes title, status, assignee, priority, tags or metadata.
func (c *Client) UpdateChatConversation(ctx context.Context, conversationID string, patch ChatConversationPatch) (*ChatConversation, error) {
	path, err := idPath(http.MethodPatch, "/chat/conversations/%s", conversationID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Conversation ChatConversation `json:"conversation"`
	}
	if err := c.chatJSON(ctx, http.MethodPatch, path, patch, &out); err != nil {
		return nil, err
	}
	return &out.Conversation, nil
}

// DeleteChatConversation deletes a conversation with its messages and files.
func (c *Client) DeleteChatConversation(ctx context.Context, conversationID string) error {
	path, err := idPath(http.MethodDelete, "/chat/conversations/%s", conversationID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// AddChatMember adds a member to a group or support chat (or changes their name or role).
func (c *Client) AddChatMember(ctx context.Context, conversationID string, m ChatMemberInput) ([]ChatMember, error) {
	path, err := idPath(http.MethodPut, "/chat/conversations/%s/members", conversationID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Members []ChatMember `json:"members"`
	}
	if err := c.chatJSON(ctx, http.MethodPut, path, m, &out); err != nil {
		return nil, err
	}
	return out.Members, nil
}

// RemoveChatMember removes a member.
func (c *Client) RemoveChatMember(ctx context.Context, conversationID, userID string) ([]ChatMember, error) {
	path, err := idPath(http.MethodDelete, "/chat/conversations/%s/members/%s", conversationID, userID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Members []ChatMember `json:"members"`
	}
	if err := c.do(ctx, http.MethodDelete, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Members, nil
}

// ChatMessages returns messages, oldest first: the newest limit (0 = 50), or before / after a seq (0 = unset).
func (c *Client) ChatMessages(ctx context.Context, conversationID string, before, after int64, limit int) ([]ChatMessage, error) {
	path, err := idPath(http.MethodGet, "/chat/conversations/%s/messages", conversationID)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	if before > 0 {
		q.Set("before", strconv.FormatInt(before, 10))
	}
	if after > 0 {
		q.Set("after", strconv.FormatInt(after, 10))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Messages []ChatMessage `json:"messages"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

// PostChatMessage posts from your backend: a system note (no UserID) or as a member.
func (c *Client) PostChatMessage(ctx context.Context, conversationID string, msg ChatPost) (*ChatMessage, error) {
	path, err := idPath(http.MethodPost, "/chat/conversations/%s/messages", conversationID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Message ChatMessage `json:"message"`
	}
	if err := c.chatJSON(ctx, http.MethodPost, path, msg, &out); err != nil {
		return nil, err
	}
	return &out.Message, nil
}

func (c *Client) ticketAction(ctx context.Context, conversationID, action string, in any) (*ChatConversation, error) {
	path, err := idPath(http.MethodPost, "/chat/conversations/%s/"+action, conversationID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Conversation ChatConversation `json:"conversation"`
	}
	if err := c.chatJSON(ctx, http.MethodPost, path, in, &out); err != nil {
		return nil, err
	}
	return &out.Conversation, nil
}

// ClaimTicket gives a support chat to an agent.
func (c *Client) ClaimTicket(ctx context.Context, conversationID, agentUserID, agentName string) (*ChatConversation, error) {
	return c.ticketAction(ctx, conversationID, "claim", map[string]string{"userId": agentUserID, "name": agentName})
}

// CloseTicket closes a support chat.
func (c *Client) CloseTicket(ctx context.Context, conversationID string) (*ChatConversation, error) {
	return c.ticketAction(ctx, conversationID, "close", nil)
}

// ReopenTicket reopens a support chat.
func (c *Client) ReopenTicket(ctx context.Context, conversationID string) (*ChatConversation, error) {
	return c.ticketAction(ctx, conversationID, "reopen", nil)
}

// ChatHistory returns who changed what in a conversation, oldest first.
func (c *Client) ChatHistory(ctx context.Context, conversationID string) ([]ChatHistoryEntry, error) {
	path, err := idPath(http.MethodGet, "/chat/conversations/%s/history", conversationID)
	if err != nil {
		return nil, err
	}
	var out struct {
		History []ChatHistoryEntry `json:"history"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.History, nil
}

// ChatTicketEmail is an email ticket's requester and thread.
type ChatTicketEmail struct {
	Address string `json:"address"`
	Name    string `json:"name"`
	Subject string `json:"subject"`
	First   string `json:"first"`
	Last    string `json:"last"`
}

// ChatEmail sets the support mailbox for email tickets. Password may be left
// empty to keep the one already set for the same address.
type ChatEmail struct {
	Address    string `json:"address"`
	Password   string `json:"password,omitempty"`
	Name       string `json:"name,omitempty"`
	Enabled    *bool  `json:"enabled,omitempty"`
	GatewayURL string `json:"gatewayUrl,omitempty"`
}

// ChatEmailChannel is the support mailbox's state (never its password).
type ChatEmailChannel struct {
	Address       string  `json:"address"`
	Name          string  `json:"name,omitempty"`
	GatewayURL    string  `json:"gatewayUrl"`
	Enabled       bool    `json:"enabled"`
	LastCheckedAt int64   `json:"lastCheckedAt"`
	LastError     *string `json:"lastError"`
	Received      int64   `json:"received"`
}

// SetChatEmail turns on email tickets: emails to the mailbox open tickets and
// replies go back by email. Live signs in to the mailbox first.
func (c *Client) SetChatEmail(ctx context.Context, in ChatEmail) (*ChatEmailChannel, error) {
	var out struct {
		Email ChatEmailChannel `json:"email"`
	}
	if err := c.chatJSON(ctx, http.MethodPut, "/chat/email", in, &out); err != nil {
		return nil, err
	}
	return &out.Email, nil
}

// ChatEmail returns the support mailbox, or nil when email tickets are off.
func (c *Client) ChatEmail(ctx context.Context) (*ChatEmailChannel, error) {
	var out struct {
		Email *ChatEmailChannel `json:"email"`
	}
	if err := c.do(ctx, http.MethodGet, "/chat/email", nil, &out); err != nil {
		return nil, err
	}
	return out.Email, nil
}

// DeleteChatEmail stops email tickets (the tickets stay).
func (c *Client) DeleteChatEmail(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/chat/email", nil, nil)
}
