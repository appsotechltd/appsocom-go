// Package sfukit lets a Go backend get join tokens for an sfukit deployment.
//
// Two ways:
//
//  1. Ask the kit (recommended): Client.CreateToken calls POST /tokens with the
//     deployment's ADMIN_API_KEY.
//  2. Sign locally: SignToken uses the deployment's JWT_SECRET, no network call.
//
// Both produce the same token the browser passes to joinRoom().
package sfukit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TokenRequest describes who is joining which room.
type TokenRequest struct {
	RoomID string `json:"roomId"`
	UserID string `json:"userId"`
	Name   string `json:"name,omitempty"`
	// Role: "teacher", "student" or "observer" get those roles' default
	// permissions. Any other name (1-32 of letters, numbers, . _ -) is a label.
	Role string `json:"role,omitempty"`
	// Permissions overrides individual permissions, e.g. Screen: sfukit.Bool(true).
	Permissions *PermissionsPatch `json:"permissions,omitempty"`
	// CanPublish (older switch) defaults to true. Set to false for view-only
	// participants. Ignored for the built-in roles.
	CanPublish *bool `json:"canPublish,omitempty"`
	// Metadata is app data shared with everyone as participant.metadata (max 4 KB as JSON).
	Metadata map[string]any `json:"metadata,omitempty"`
	// TTLSeconds defaults to 7200 (2h); allowed 60..86400.
	TTLSeconds int `json:"ttlSeconds,omitempty"`
	// MaxParticipants limits the room when this token joins (2 = one-on-one). 0 = no limit.
	MaxParticipants int `json:"maxParticipants,omitempty"`
	// E2EE requires end-to-end encryption: the client must join with a key
	// (which never goes in the token) and the room refuses its tracks until it
	// says it encrypts.
	E2EE bool `json:"e2ee,omitempty"`
	// DataSaver is the data-saver preset the client starts with: "low" (360p,
	// speaker video only), "minimal" (180p), "audio" (no camera video) or
	// "off"/"" (normal). The user can still change it during the call.
	DataSaver string `json:"dataSaver,omitempty"`
}

// Permissions is what a participant may do; the server enforces it.
type Permissions struct {
	Mic      bool `json:"mic"`
	Camera   bool `json:"camera"`
	Screen   bool `json:"screen"`
	Moderate bool `json:"moderate"`
}

// PermissionsPatch changes some permissions; nil fields stay as they are.
type PermissionsPatch struct {
	Mic      *bool `json:"mic,omitempty"`
	Camera   *bool `json:"camera,omitempty"`
	Screen   *bool `json:"screen,omitempty"`
	Moderate *bool `json:"moderate,omitempty"`
}

// RolePermissions are the defaults of the built-in classroom roles.
var RolePermissions = map[string]Permissions{
	"teacher":  {Mic: true, Camera: true, Screen: true, Moderate: true},
	"student":  {Mic: true, Camera: true, Screen: false, Moderate: false},
	"observer": {},
}

func (p Permissions) apply(patch *PermissionsPatch) Permissions {
	if patch == nil {
		return p
	}
	if patch.Mic != nil {
		p.Mic = *patch.Mic
	}
	if patch.Camera != nil {
		p.Camera = *patch.Camera
	}
	if patch.Screen != nil {
		p.Screen = *patch.Screen
	}
	if patch.Moderate != nil {
		p.Moderate = *patch.Moderate
	}
	return p
}

// Token is a join token and its expiry (unix seconds).
type Token struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"`
}

// Client talks to one sfukit deployment (one Cloudflare account).
type Client struct {
	BaseURL    string // e.g. https://video.client.com or https://api.client.com/video
	AdminKey   string // ADMIN_API_KEY of that deployment
	HTTPClient *http.Client
}

// NewClient returns a Client with a 10s timeout.
func NewClient(baseURL, adminKey string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		AdminKey:   adminKey,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// CreateToken asks the kit to mint a join token.
func (c *Client) CreateToken(ctx context.Context, req TokenRequest) (*Token, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var out Token
	if err := c.do(ctx, http.MethodPost, "/tokens", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Participant is a subset of what GET /rooms/:id returns.
type Participant struct {
	ID string `json:"id"`
	// Kind is "user" (or empty), "ingress" (an OBS/WHIP stream) or "bot".
	Kind        string      `json:"kind,omitempty"`
	UserID      string      `json:"userId"`
	Name        string      `json:"name"`
	Role        string      `json:"role"`
	Permissions Permissions `json:"permissions"`
	CanPublish  bool        `json:"canPublish"`
	// Group is the breakout group id, or nil in the main room.
	Group *string `json:"group"`
	// HandRaisedAt is when the hand went up (ms since epoch), or nil.
	HandRaisedAt *int64 `json:"handRaisedAt"`
	// Metadata set by your backend (token or SetParticipantMetadata), or nil.
	Metadata map[string]any `json:"metadata,omitempty"`
	JoinedAt int64          `json:"joinedAt"`
	State    struct {
		MicOn    bool `json:"micOn"`
		CameraOn bool `json:"cameraOn"`
		ScreenOn bool `json:"screenOn"`
	} `json:"state"`
}

// ListParticipants returns who is currently in a room.
func (c *Client) ListParticipants(ctx context.Context, roomID string) ([]Participant, error) {
	path, err := idPath(http.MethodGet, "/rooms/%s", roomID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Participants []Participant `json:"participants"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Participants, nil
}

// Kick disconnects every connection of userID from roomID.
func (c *Client) Kick(ctx context.Context, roomID, userID string) error {
	path, err := idPath(http.MethodDelete, "/rooms/%s/participants/%s", roomID, userID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// Ban disconnects userID and keeps them out of roomID until it empties (or Unban).
func (c *Client) Ban(ctx context.Context, roomID, userID string) error {
	path, err := idPath(http.MethodDelete, "/rooms/%s/participants/%s", roomID, userID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path+"?ban=true", nil, nil)
}

// Unban lets a banned user back into roomID.
func (c *Client) Unban(ctx context.Context, roomID, userID string) error {
	path, err := idPath(http.MethodDelete, "/rooms/%s/bans/%s", roomID, userID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// PostChat posts a chat message from your backend; to = "" sends it to everyone,
// otherwise privately to that userID.
func (c *Client) PostChat(ctx context.Context, roomID, text, to string) error {
	path, err := idPath(http.MethodPost, "/rooms/%s/chat", roomID)
	if err != nil {
		return err
	}
	payload := map[string]string{"text": text}
	if to != "" {
		payload["to"] = to
	}
	body, _ := json.Marshal(payload)
	return c.do(ctx, http.MethodPost, path, body, nil)
}

// SetChatEnabled turns chat on/off for everyone without the moderate permission.
func (c *Client) SetChatEnabled(ctx context.Context, roomID string, enabled bool) error {
	path, err := idPath(http.MethodPatch, "/rooms/%s", roomID)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]bool{"chat": enabled})
	return c.do(ctx, http.MethodPatch, path, body, nil)
}

// SetSpotlight puts up to 6 userIDs in the spotlight for everyone in roomID (empty clears).
func (c *Client) SetSpotlight(ctx context.Context, roomID string, userIDs []string) error {
	path, err := idPath(http.MethodPatch, "/rooms/%s", roomID)
	if err != nil {
		return err
	}
	if userIDs == nil {
		userIDs = []string{}
	}
	body, _ := json.Marshal(map[string][]string{"spotlight": userIDs})
	return c.do(ctx, http.MethodPatch, path, body, nil)
}

// SetWaitingRoom turns roomID's waiting room on/off (off admits everyone waiting).
func (c *Client) SetWaitingRoom(ctx context.Context, roomID string, on bool) error {
	path, err := idPath(http.MethodPatch, "/rooms/%s", roomID)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]bool{"waitingRoom": on})
	return c.do(ctx, http.MethodPatch, path, body, nil)
}

// Admit lets userID in from roomID's waiting room ("*" = everyone waiting).
// Returns how many connections were admitted.
func (c *Client) Admit(ctx context.Context, roomID, userID string) (int, error) {
	path, err := idPath(http.MethodPost, "/rooms/%s/admit", roomID)
	if err != nil {
		return 0, err
	}
	body, _ := json.Marshal(map[string]string{"userId": userID})
	var out struct {
		Admitted int `json:"admitted"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return 0, err
	}
	return out.Admitted, nil
}

// AttendanceUser is one row of an attendance report.
type AttendanceUser struct {
	UserID        string `json:"userId"`
	Name          string `json:"name"`
	Role          string `json:"role"`
	FirstJoinedAt int64  `json:"firstJoinedAt"`
	LastLeftAt    *int64 `json:"lastLeftAt"`
	TotalSeconds  int64  `json:"totalSeconds"`
	Sessions      int    `json:"sessions"`
	Present       bool   `json:"present"`
}

// Attendance returns who attended roomID and for how long (kept 30 days after
// the last activity). Times are ms since epoch.
func (c *Client) Attendance(ctx context.Context, roomID string) ([]AttendanceUser, error) {
	path, err := idPath(http.MethodGet, "/rooms/%s/attendance", roomID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Users []AttendanceUser `json:"users"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Users, nil
}

// BreakoutGroup names a breakout group and who goes in it.
type BreakoutGroup struct {
	ID      string   `json:"id,omitempty"`
	Name    string   `json:"name"`
	UserIDs []string `json:"userIds"`
}

// StartBreakouts splits roomID into groups (replacing running breakouts).
// Users not listed stay in the main room. Returns the groups with their ids.
func (c *Client) StartBreakouts(ctx context.Context, roomID string, groups []BreakoutGroup) ([]BreakoutGroup, error) {
	path, err := idPath(http.MethodPut, "/rooms/%s/breakouts", roomID)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{"groups": groups})
	if err != nil {
		return nil, err
	}
	var out struct {
		Groups []BreakoutGroup `json:"groups"`
	}
	if err := c.do(ctx, http.MethodPut, path, body, &out); err != nil {
		return nil, err
	}
	return out.Groups, nil
}

// EndBreakouts brings everyone in roomID back to the main room.
func (c *Client) EndBreakouts(ctx context.Context, roomID string) error {
	path, err := idPath(http.MethodDelete, "/rooms/%s/breakouts", roomID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// ---------- AI lessons (an AI tutor, or an AI-taught class) ----------

// LessonStart is what StartLesson sends: Mode "tutor" or "class", Lesson the plan
// ({"title", "subject", "level", "curriculum", "objectives", "sections" or "text"}),
// and optional settings (learnerId, floor, scope, maxMinutes, llmModel, voice, avatar…) in Options.
type LessonStart struct {
	Mode    string
	Lesson  map[string]any
	Options map[string]any
}

// LessonStarted is StartLesson's answer.
type LessonStarted struct {
	LessonID string `json:"lessonId"`
	AgentID  string `json:"agentId"`
}

// StartLesson starts an AI lesson in roomID.
func (c *Client) StartLesson(ctx context.Context, roomID string, in LessonStart) (*LessonStarted, error) {
	path, err := idPath(http.MethodPost, "/rooms/%s/lessons", roomID)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	for k, v := range in.Options {
		m[k] = v
	}
	m["mode"] = in.Mode
	if in.Lesson != nil { // nil with Options["planId"]: teach an approved plan from the lesson library
		m["lesson"] = in.Lesson
	}
	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var out LessonStarted
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---------- lesson library: drafts, review, courses, timetables ----------

func (c *Client) jsonCall(ctx context.Context, method, path string, in any) (map[string]any, error) {
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = b
	}
	var out map[string]any
	err := c.do(ctx, method, path, body, &out)
	return out, err
}

func listOf(m map[string]any, key string, err error) ([]map[string]any, error) {
	if err != nil {
		return nil, err
	}
	raw, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if x, ok := v.(map[string]any); ok {
			out = append(out, x)
		}
	}
	return out, nil
}

// LessonSettings returns {requireReview, defaults}. With requireReview on (the default) only approved plans are taught.
func (c *Client) LessonSettings(ctx context.Context) (map[string]any, error) {
	return c.jsonCall(ctx, http.MethodGet, "/lesson-settings", nil)
}

// SetLessonSettings changes requireReview and/or defaults.
func (c *Client) SetLessonSettings(ctx context.Context, settings map[string]any) (map[string]any, error) {
	return c.jsonCall(ctx, http.MethodPut, "/lesson-settings", settings)
}

// ListLessonPlans lists the library; status is "", "draft", "approved" or "archived".
func (c *Client) ListLessonPlans(ctx context.Context, status string) ([]map[string]any, error) {
	path := "/lesson-plans"
	if status != "" {
		path += "?status=" + url.QueryEscape(status)
	}
	m, err := c.jsonCall(ctx, http.MethodGet, path, nil)
	return listOf(m, "plans", err)
}

// CreateLessonPlan adds a written "plan" (a draft), or material for the AI to draft from:
// "text", "link" (Google Docs/Slides), "topic", "curriculum", "exam". AI drafts come back
// with draftState "processing"; the lesson_plan_ready webhook says when they're ready.
func (c *Client) CreateLessonPlan(ctx context.Context, input map[string]any) (map[string]any, error) {
	return c.jsonCall(ctx, http.MethodPost, "/lesson-plans", input)
}

// GetLessonPlan returns one plan.
func (c *Client) GetLessonPlan(ctx context.Context, planID string) (map[string]any, error) {
	path, err := segPath(http.MethodGet, "/lesson-plans/%s", planID)
	if err != nil {
		return nil, err
	}
	return c.jsonCall(ctx, http.MethodGet, path, nil)
}

// UpdateLessonPlan edits a plan; an approved plan keeps teaching its approved version until approved again.
func (c *Client) UpdateLessonPlan(ctx context.Context, planID string, changes map[string]any) (map[string]any, error) {
	path, err := segPath(http.MethodPut, "/lesson-plans/%s", planID)
	if err != nil {
		return nil, err
	}
	return c.jsonCall(ctx, http.MethodPut, path, changes)
}

// ReviseLessonPlan asks the AI to change a plan, e.g. "make section 2 simpler".
func (c *Client) ReviseLessonPlan(ctx context.Context, planID, instruction string) (map[string]any, error) {
	path, err := segPath(http.MethodPost, "/lesson-plans/%s/revise", planID)
	if err != nil {
		return nil, err
	}
	return c.jsonCall(ctx, http.MethodPost, path, map[string]any{"instruction": instruction})
}

// ApproveLessonPlan approves the plan as it is now (a new version). Start lessons with Options["planId"].
func (c *Client) ApproveLessonPlan(ctx context.Context, planID string) (map[string]any, error) {
	path, err := segPath(http.MethodPost, "/lesson-plans/%s/approve", planID)
	if err != nil {
		return nil, err
	}
	return c.jsonCall(ctx, http.MethodPost, path, nil)
}

// LessonPlanVersions lists a plan's approved versions.
func (c *Client) LessonPlanVersions(ctx context.Context, planID string) ([]map[string]any, error) {
	path, err := segPath(http.MethodGet, "/lesson-plans/%s/versions", planID)
	if err != nil {
		return nil, err
	}
	m, err := c.jsonCall(ctx, http.MethodGet, path, nil)
	return listOf(m, "versions", err)
}

// TrialLessonPlan starts a 5-minute tutor run of the plan as it is now (even unapproved); roomID may be "".
func (c *Client) TrialLessonPlan(ctx context.Context, planID, roomID string) (map[string]any, error) {
	in := map[string]any{}
	if roomID != "" {
		in["roomId"] = roomID
	}
	path, err := segPath(http.MethodPost, "/lesson-plans/%s/trial", planID)
	if err != nil {
		return nil, err
	}
	return c.jsonCall(ctx, http.MethodPost, path, in)
}

// ArchiveLessonPlan archives a plan.
func (c *Client) ArchiveLessonPlan(ctx context.Context, planID string) error {
	path, err := segPath(http.MethodDelete, "/lesson-plans/%s", planID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// ListCourses lists courses.
func (c *Client) ListCourses(ctx context.Context) ([]map[string]any, error) {
	m, err := c.jsonCall(ctx, http.MethodGet, "/courses", nil)
	return listOf(m, "courses", err)
}

// SaveCourse creates a course {title, subject, level, plans: planIDs in order}, or updates the one named by course["id"].
func (c *Client) SaveCourse(ctx context.Context, course map[string]any) (map[string]any, error) {
	body := map[string]any{}
	for k, v := range course {
		if k != "id" {
			body[k] = v
		}
	}
	if id, _ := course["id"].(string); id != "" {
		path, err := segPath(http.MethodPut, "/courses/%s", id)
		if err != nil {
			return nil, err
		}
		return c.jsonCall(ctx, http.MethodPut, path, body)
	}
	return c.jsonCall(ctx, http.MethodPost, "/courses", body)
}

// DeleteCourse deletes a course (refused while a timetable uses it).
func (c *Client) DeleteCourse(ctx context.Context, courseID string) error {
	path, err := segPath(http.MethodDelete, "/courses/%s", courseID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// ListTimetables lists timetables.
func (c *Client) ListTimetables(ctx context.Context) ([]map[string]any, error) {
	m, err := c.jsonCall(ctx, http.MethodGet, "/timetables", nil)
	return listOf(m, "timetables", err)
}

// SaveTimetable creates a timetable, or updates the one named by timetable["id"]. Two lessons in
// one room at the same time give a 409 error with the clashes, unless allowClashes.
func (c *Client) SaveTimetable(ctx context.Context, timetable map[string]any, allowClashes bool) (map[string]any, error) {
	body := map[string]any{}
	for k, v := range timetable {
		if k != "id" {
			body[k] = v
		}
	}
	q := ""
	if allowClashes {
		q = "?allowClashes=true"
	}
	if id, _ := timetable["id"].(string); id != "" {
		path, err := segPath(http.MethodPut, "/timetables/%s", id)
		if err != nil {
			return nil, err
		}
		return c.jsonCall(ctx, http.MethodPut, path+q, body)
	}
	return c.jsonCall(ctx, http.MethodPost, "/timetables"+q, body)
}

// DeleteTimetable deletes a timetable.
func (c *Client) DeleteTimetable(ctx context.Context, timetableID string) error {
	path, err := segPath(http.MethodDelete, "/timetables/%s", timetableID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// TimetableRuns lists scheduled lessons in the next days days; timetableID may be "".
func (c *Client) TimetableRuns(ctx context.Context, days int, timetableID string) ([]map[string]any, error) {
	q := url.Values{}
	if days > 0 {
		q.Set("days", strconv.Itoa(days))
	}
	if timetableID != "" {
		q.Set("timetableId", timetableID)
	}
	path := "/timetable-runs"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	m, err := c.jsonCall(ctx, http.MethodGet, path, nil)
	return listOf(m, "runs", err)
}

// StartTimetableRun starts a scheduled lesson now.
func (c *Client) StartTimetableRun(ctx context.Context, key string) (map[string]any, error) {
	path, err := segPath(http.MethodPost, "/timetable-runs/%s/start", key)
	if err != nil {
		return nil, err
	}
	return c.jsonCall(ctx, http.MethodPost, path, nil)
}

// SkipTimetableRun skips a scheduled lesson.
func (c *Client) SkipTimetableRun(ctx context.Context, key string) (map[string]any, error) {
	path, err := segPath(http.MethodPost, "/timetable-runs/%s/skip", key)
	if err != nil {
		return nil, err
	}
	return c.jsonCall(ctx, http.MethodPost, path, nil)
}

// GetLesson returns a running lesson's live state.
func (c *Client) GetLesson(ctx context.Context, roomID, lessonID string) (map[string]any, error) {
	path, err := idPath(http.MethodGet, "/rooms/%s/lessons/%s", roomID, lessonID)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// ControlLesson steers a lesson: control["action"] is pause, resume, end, goto, call-on, floor-policy, scope or note.
func (c *Client) ControlLesson(ctx context.Context, roomID, lessonID string, control map[string]any) error {
	path, err := idPath(http.MethodPost, "/rooms/%s/lessons/%s/control", roomID, lessonID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(control)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, path, body, nil)
}

// StopLesson stops a lesson; its report is written.
func (c *Client) StopLesson(ctx context.Context, roomID, lessonID string) error {
	path, err := idPath(http.MethodDelete, "/rooms/%s/lessons/%s", roomID, lessonID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// LessonReport returns a finished lesson's report (summary, learners, alerts, transcript).
func (c *Client) LessonReport(ctx context.Context, lessonID string) (map[string]any, error) {
	path, err := idPath(http.MethodGet, "/lessons/%s/report", lessonID)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// StartPresenter starts an AI Presenter from an approved presentation kit. options: planId
// (or lesson), lead ("ai" or "human"), name, voice, avatar, reportEmails, webhookUrl, maxMinutes.
// The result has presentationId, lessonId and agentId. Stop it with StopLesson.
func (c *Client) StartPresenter(ctx context.Context, roomID string, options map[string]any) (map[string]any, error) {
	path, err := idPath(http.MethodPost, "/rooms/%s/presenter", roomID)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(options)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.do(ctx, http.MethodPost, path, body, &out)
	return out, err
}

// SetPresenterLead changes who leads: "ai" or "human".
func (c *Client) SetPresenterLead(ctx context.Context, roomID, presentationID, lead string) error {
	return c.ControlLesson(ctx, roomID, presentationID, map[string]any{"action": "lead", "lead": lead})
}

// PresenterBrowser controls the live demo's cloud browser: "take", "give" or "close".
func (c *Client) PresenterBrowser(ctx context.Context, roomID, presentationID, action string) error {
	return c.ControlLesson(ctx, roomID, presentationID, map[string]any{"action": "browser", "browser": action})
}

// PresenterBrowserOp sends one browser step, such as {"op": "click-at", "x": 0.4, "y": 0.2}
// or {"op": "scroll", "direction": "down"}. Only whoever has taken the browser can send them.
func (c *Client) PresenterBrowserOp(ctx context.Context, roomID, presentationID string, op map[string]any) error {
	return c.ControlLesson(ctx, roomID, presentationID, map[string]any{"action": "browser", "browser": "op", "op": op})
}

// ListPresentationReports returns presentations that ended, newest first ({"presentations": [...]}).
// planID is optional ("" for all kits).
func (c *Client) ListPresentationReports(ctx context.Context, planID string) (map[string]any, error) {
	path := "/presentation-reports"
	if planID != "" {
		path += "?planId=" + url.QueryEscape(planID)
	}
	var out map[string]any
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// PresentationReport returns one presentation: summary, recommendations, questions and suggestions.
func (c *Client) PresentationReport(ctx context.Context, reportID string) (map[string]any, error) {
	path, err := idPath(http.MethodGet, "/presentation-reports/%s", reportID)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// ReviewSuggestion marks a suggestion (its index in the report) "new", "reviewed", "accepted" or "dismissed".
func (c *Client) ReviewSuggestion(ctx context.Context, reportID string, index int, status string) (map[string]any, error) {
	path, err := idPath(http.MethodPut, "/presentation-reports/%s/suggestions/", reportID)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]string{"status": status})
	var out map[string]any
	err = c.do(ctx, http.MethodPut, path+strconv.Itoa(index), body, &out)
	return out, err
}

// ---- self-running presentations: a link the AI Presenter presents on its own, privately to each visitor

// PresentationVisitor is what a visitor typed before their presentation.
type PresentationVisitor struct {
	Name    string `json:"name,omitempty"`
	Email   string `json:"email,omitempty"`
	Company string `json:"company,omitempty"`
	Phone   string `json:"phone,omitempty"`
}

// PresentationSession is a visitor's private room and join token.
type PresentationSession struct {
	SessionID string `json:"sessionId"`
	RoomID    string `json:"roomId"`
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"`
	Title     string `json:"title"`
}

// CreatePresentationLink makes a link from an approved kit. options must have planId; also title,
// name, voice, avatar, reportEmails, askDetails, maxMinutes (30), perDay (50), maxConcurrent (5), active.
func (c *Client) CreatePresentationLink(ctx context.Context, options map[string]any) (map[string]any, error) {
	return c.jsonCall(ctx, http.MethodPost, "/presentation-links", options)
}

// ListPresentationLinks returns {"links": [...]}.
func (c *Client) ListPresentationLinks(ctx context.Context) (map[string]any, error) {
	return c.jsonCall(ctx, http.MethodGet, "/presentation-links", nil)
}

// PresentationLink returns one link.
func (c *Client) PresentationLink(ctx context.Context, linkID string) (map[string]any, error) {
	return c.jsonCall(ctx, http.MethodGet, "/presentation-links/"+url.PathEscape(linkID), nil)
}

// UpdatePresentationLink changes a link; fields left out stay ("active": false closes it).
func (c *Client) UpdatePresentationLink(ctx context.Context, linkID string, patch map[string]any) (map[string]any, error) {
	return c.jsonCall(ctx, http.MethodPatch, "/presentation-links/"+url.PathEscape(linkID), patch)
}

// DeletePresentationLink deletes a link.
func (c *Client) DeletePresentationLink(ctx context.Context, linkID string) error {
	return c.do(ctx, http.MethodDelete, "/presentation-links/"+url.PathEscape(linkID), nil, nil)
}

// StartPresentationSession is called when a visitor opens the link. The presenter starts when they
// join RoomID with Token. Past the link's limits the API answers 429.
func (c *Client) StartPresentationSession(ctx context.Context, linkID string, visitor PresentationVisitor) (*PresentationSession, error) {
	body, _ := json.Marshal(map[string]any{"visitor": visitor})
	var out PresentationSession
	if err := c.do(ctx, http.MethodPost, "/presentation-links/"+url.PathEscape(linkID)+"/sessions", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListPresentationSessions returns visitors and their leads, newest first ({"sessions": [...]}).
// linkID is optional ("" for all links).
func (c *Client) ListPresentationSessions(ctx context.Context, linkID string) (map[string]any, error) {
	path := "/presentation-sessions"
	if linkID != "" {
		path += "?linkId=" + url.QueryEscape(linkID)
	}
	return c.jsonCall(ctx, http.MethodGet, path, nil)
}

// PresentationSessionInfo returns one visitor's session with its leads.
func (c *Client) PresentationSessionInfo(ctx context.Context, sessionID string) (map[string]any, error) {
	return c.jsonCall(ctx, http.MethodGet, "/presentation-sessions/"+url.PathEscape(sessionID), nil)
}

// JoinPresentationSession returns a host token for a colleague to step into a visitor's presentation.
func (c *Client) JoinPresentationSession(ctx context.Context, sessionID, name, userID string) (map[string]any, error) {
	in := map[string]any{"name": name}
	if userID != "" {
		in["userId"] = userID
	}
	return c.jsonCall(ctx, http.MethodPost, "/presentation-sessions/"+url.PathEscape(sessionID)+"/join", in)
}

// LearnerProgress returns a tutor learner's saved progress.
func (c *Client) LearnerProgress(ctx context.Context, learnerID string) (map[string]any, error) {
	path, err := segPath(http.MethodGet, "/learners/%s/progress", learnerID)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// MoveToGroup moves userID to breakout group id groupID ("" = main room).
func (c *Client) MoveToGroup(ctx context.Context, roomID, userID, groupID string) error {
	path, err := idPath(http.MethodPost, "/rooms/%s/move", roomID)
	if err != nil {
		return err
	}
	var group any
	if groupID != "" {
		group = groupID
	}
	body, _ := json.Marshal(map[string]any{"userId": userID, "group": group})
	return c.do(ctx, http.MethodPost, path, body, nil)
}

// LowerHand lowers userID's raised hand; userID "" lowers everyone's.
func (c *Client) LowerHand(ctx context.Context, roomID, userID string) error {
	path, err := idPath(http.MethodDelete, "/rooms/%s/hands", roomID)
	if userID != "" {
		path, err = idPath(http.MethodDelete, "/rooms/%s/hands/%s", roomID, userID)
	}
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// LockRoom stops anyone without the moderate permission from joining roomID (false unlocks).
func (c *Client) LockRoom(ctx context.Context, roomID string, locked bool) error {
	path, err := idPath(http.MethodPatch, "/rooms/%s", roomID)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]bool{"locked": locked})
	return c.do(ctx, http.MethodPatch, path, body, nil)
}

// Mute turns off devices ("mic", "camera", "screen") of userID, or of everyone
// without the moderate permission when userID is "*". With lock, the permissions
// are taken away too. Returns how many connections were muted.
func (c *Client) Mute(ctx context.Context, roomID, userID string, devices []string, lock bool) (int, error) {
	path, err := idPath(http.MethodPost, "/rooms/%s/mute", roomID)
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(map[string]any{"userId": userID, "devices": devices, "lock": lock})
	if err != nil {
		return 0, err
	}
	var out struct {
		Muted int `json:"muted"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return 0, err
	}
	return out.Muted, nil
}

// SetPermissions changes what userID may do in roomID, for current and future
// connections until the room empties. A nil patch resets to the token's permissions.
// Returns how many live connections were updated.
func (c *Client) SetPermissions(ctx context.Context, roomID, userID string, patch *PermissionsPatch) (int, error) {
	path, err := idPath(http.MethodPatch, "/rooms/%s/participants/%s", roomID, userID)
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(map[string]any{"permissions": patch})
	if err != nil {
		return 0, err
	}
	var out struct {
		Updated int `json:"updated"`
	}
	if err := c.do(ctx, http.MethodPatch, path, body, &out); err != nil {
		return 0, err
	}
	return out.Updated, nil
}

// SetRole changes a user's role during the call, e.g. promote an "observer" to
// "student". For the built-in roles their permissions become that role's
// defaults, with patch (may be nil) applied on top. Lasts until the room empties.
func (c *Client) SetRole(ctx context.Context, roomID, userID, role string, patch *PermissionsPatch) (int, error) {
	path, err := idPath(http.MethodPatch, "/rooms/%s/participants/%s", roomID, userID)
	if err != nil {
		return 0, err
	}
	req := map[string]any{"role": role}
	if patch != nil {
		req["permissions"] = patch
	}
	body, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	var out struct {
		Updated int `json:"updated"`
	}
	if err := c.do(ctx, http.MethodPatch, path, body, &out); err != nil {
		return 0, err
	}
	return out.Updated, nil
}

const maxMetadataBytes = 4096

// SetRoomMetadata replaces the room's metadata (shared with everyone in it as
// roomState.metadata). nil clears it.
func (c *Client) SetRoomMetadata(ctx context.Context, roomID string, metadata map[string]any) error {
	path, err := idPath(http.MethodPatch, "/rooms/%s", roomID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"metadata": metadata})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPatch, path, body, nil)
}

// SetParticipantMetadata replaces a user's metadata (shared with everyone as
// participant.metadata) for all their connections. nil clears it. Returns how
// many connections were updated (it also applies when they join later).
func (c *Client) SetParticipantMetadata(ctx context.Context, roomID, userID string, metadata map[string]any) (int, error) {
	return c.patchParticipant(ctx, roomID, userID, map[string]any{"metadata": metadata})
}

// TrackPermissions chooses who may receive a participant's tracks.
// AllowAll true (the default) = everyone in the room.
type TrackPermissions struct {
	AllowAll bool              `json:"allowAll"`
	Allow    []TrackPermission `json:"allow,omitempty"`
}

// TrackPermission lets one user receive some (Kinds empty = all) of the tracks.
// Kinds: "audio", "video", "screenVideo", "screenAudio".
type TrackPermission struct {
	UserID string   `json:"userId"`
	Kinds  []string `json:"kinds,omitempty"`
}

// SetTrackPermissions sets who may receive userID's tracks (nil = everyone again).
// Enforced by the server; people who lose access stop receiving within seconds.
func (c *Client) SetTrackPermissions(ctx context.Context, roomID, userID string, tp *TrackPermissions) (int, error) {
	return c.patchParticipant(ctx, roomID, userID, map[string]any{"trackPermissions": tp})
}

// SetParticipantName changes a user's display name during the call.
func (c *Client) SetParticipantName(ctx context.Context, roomID, userID, name string) (int, error) {
	return c.patchParticipant(ctx, roomID, userID, map[string]any{"name": name})
}

func (c *Client) patchParticipant(ctx context.Context, roomID, userID string, req map[string]any) (int, error) {
	path, err := idPath(http.MethodPatch, "/rooms/%s/participants/%s", roomID, userID)
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	var out struct {
		Updated int `json:"updated"`
	}
	if err := c.do(ctx, http.MethodPatch, path, body, &out); err != nil {
		return 0, err
	}
	return out.Updated, nil
}

// Recording is a recording in progress.
type Recording struct {
	ID        string `json:"id"`
	StartedAt int64  `json:"startedAt"`
	StartedBy string `json:"startedBy"`
	Video     bool   `json:"video"`
}

// RecordingOptions for StartRecording. Zero value: audio only.
type RecordingOptions struct {
	// Video records audio + video with the recorder bot (needs the kit deployed with wrangler.video.jsonc).
	Video bool `json:"video,omitempty"`
	// MaxMinutes stops a video recording after this long (default 360).
	MaxMinutes int `json:"maxMinutes,omitempty"`
}

// RecordingFile is one participant's track in R2: mono 48 kHz WAV (audio-only
// recordings) or WebM (video recordings; Opus audio, VP8/VP9 video).
type RecordingFile struct {
	Key        string `json:"key"`
	UserID     string `json:"userId"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Format     string `json:"format"`
	Codec      string `json:"codec"`
	SampleRate int    `json:"sampleRate,omitempty"`
	Channels   int    `json:"channels,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	StartedAt  int64  `json:"startedAt"`
	EndedAt    int64  `json:"endedAt"`
	DurationMs int64  `json:"durationMs"`
	Bytes      int64  `json:"bytes"`
}

// RecordingManifest is written to R2 (manifest.json) when a recording stops.
type RecordingManifest struct {
	RoomID      string          `json:"roomId"`
	RecordingID string          `json:"recordingId"`
	StartedAt   int64           `json:"startedAt"`
	StoppedAt   int64           `json:"stoppedAt"`
	StartedBy   string          `json:"startedBy"`
	StopReason  string          `json:"stopReason"`
	Files       []RecordingFile `json:"files"`
	Errors      []string        `json:"errors"`
	ManifestKey string          `json:"manifestKey"`
}

// StartRecording starts recording the room to R2. Everyone in the room is told.
func (c *Client) StartRecording(ctx context.Context, roomID string, opts RecordingOptions) (*Recording, error) {
	path, err := idPath(http.MethodPost, "/rooms/%s/recording", roomID)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(opts)
	if err != nil {
		return nil, err
	}
	var out struct {
		Recording *Recording `json:"recording"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return out.Recording, nil
}

// StopRecording stops the recording and returns its manifest.
func (c *Client) StopRecording(ctx context.Context, roomID string) (*RecordingManifest, error) {
	path, err := idPath(http.MethodDelete, "/rooms/%s/recording", roomID)
	if err != nil {
		return nil, err
	}
	var out RecordingManifest
	if err := c.do(ctx, http.MethodDelete, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RecordingStatus returns the recording in progress, or nil.
func (c *Client) RecordingStatus(ctx context.Context, roomID string) (*Recording, error) {
	path, err := idPath(http.MethodGet, "/rooms/%s/recording", roomID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Recording *Recording `json:"recording"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Recording, nil
}

// CloseRoom disconnects everyone from roomID.
func (c *Client) CloseRoom(ctx context.Context, roomID string) error {
	path, err := idPath(http.MethodDelete, "/rooms/%s", roomID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// IngressRequest creates an ingest point. All fields are optional.
type IngressRequest struct {
	Name   string `json:"name,omitempty"`
	UserID string `json:"userId,omitempty"`
	// Protocol: "whip" (default; OBS publishes straight into the room) or
	// "rtmp" (RTMPS/SRT to a Cloudflare Stream live input, brought in by the bot).
	Protocol string `json:"protocol,omitempty"`
}

// RTMPSEndpoint is where an RTMP encoder sends: OBS Service "Custom", Server = URL, Stream Key = StreamKey.
type RTMPSEndpoint struct {
	URL       string `json:"url"`
	StreamKey string `json:"streamKey"`
}

// SRTEndpoint is the SRT alternative to RTMPS.
type SRTEndpoint struct {
	URL        string `json:"url"`
	StreamID   string `json:"streamId"`
	Passphrase string `json:"passphrase"`
}

// Ingress is an ingest point. Keys (StreamKey, RTMPS, SRT) are only set by CreateIngress.
type Ingress struct {
	IngressID string `json:"ingressId"`
	RoomID    string `json:"roomId"`
	Protocol  string `json:"protocol"` // "whip" or "rtmp"
	Name      string `json:"name"`
	UserID    string `json:"userId"`
	// WHIPURL (protocol "whip") goes into OBS: Settings > Stream > Service "WHIP" > Server.
	WHIPURL string `json:"whipUrl,omitempty"`
	// Protocol "rtmp": the Cloudflare Stream live input, the encoder's connection
	// to it ("connected", "client_disconnect", ...; empty = never connected), and where to send.
	StreamInputID string         `json:"streamInputId,omitempty"`
	EncoderStatus string         `json:"encoderStatus,omitempty"`
	RTMPS         *RTMPSEndpoint `json:"rtmps,omitempty"`
	SRT           *SRTEndpoint   `json:"srt,omitempty"`
	// StreamKey goes into OBS as the Bearer Token. Shown once; store it like a password.
	StreamKey string `json:"streamKey,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	Live      bool   `json:"live"`
	LiveSince int64  `json:"liveSince,omitempty"`
}

// CreateIngress makes an ingest point for a room (WHIP, or RTMP/SRT via Cloudflare Stream).
func (c *Client) CreateIngress(ctx context.Context, roomID string, req IngressRequest) (*Ingress, error) {
	path, err := idPath(http.MethodPost, "/rooms/%s/ingress", roomID)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(req)
	var out Ingress
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListIngress returns a room's ingest points and whether each is live.
func (c *Client) ListIngress(ctx context.Context, roomID string) ([]Ingress, error) {
	path, err := idPath(http.MethodGet, "/rooms/%s/ingress", roomID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Ingress []Ingress `json:"ingress"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Ingress, nil
}

// DeleteIngress removes an ingest point and ends its stream if live.
func (c *Client) DeleteIngress(ctx context.Context, roomID, ingressID string) error {
	path, err := idPath(http.MethodDelete, "/rooms/%s/ingress/%s", roomID, ingressID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// EgressDestination is where a room's live stream goes. Set URL, or Platform
// ("youtube", "facebook") to use that service's ingest server.
type EgressDestination struct {
	Platform  string `json:"platform,omitempty"`
	URL       string `json:"url,omitempty"`
	StreamKey string `json:"streamKey,omitempty"`
	Name      string `json:"name,omitempty"`
	// ID is set in responses (Cloudflare Stream output id, or "direct").
	ID string `json:"id,omitempty"`
}

// EgressRequest starts streaming a room out. All fields are optional.
type EgressRequest struct {
	Destinations []EgressDestination `json:"destinations,omitempty"`
	// Via is "cloudflare-stream" (default when the Worker has Stream configured) or "direct".
	Via    string `json:"via,omitempty"`
	Record bool   `json:"record,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// Egress is a room being streamed out.
type Egress struct {
	EgressID      string              `json:"egressId"`
	RoomID        string              `json:"roomId"`
	Status        string              `json:"status"` // starting, live, stopped, failed
	Via           string              `json:"via"`
	Layout        string              `json:"layout"`
	Width         int                 `json:"width"`
	Height        int                 `json:"height"`
	Record        bool                `json:"record"`
	Playback      map[string]string   `json:"playback,omitempty"` // hls, dash, iframe
	StreamInputID string              `json:"streamInputId,omitempty"`
	Destinations  []EgressDestination `json:"destinations"`
	CreatedAt     int64               `json:"createdAt"`
	StoppedAt     int64               `json:"stoppedAt,omitempty"`
	Error         string              `json:"error,omitempty"`
}

// StartEgress streams a room out (Cloudflare Stream and/or RTMP destinations).
func (c *Client) StartEgress(ctx context.Context, roomID string, req EgressRequest) (*Egress, error) {
	path, err := idPath(http.MethodPost, "/rooms/%s/egress", roomID)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(req)
	var out Egress
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListEgress returns a room's live streams out, with fresh status.
func (c *Client) ListEgress(ctx context.Context, roomID string) ([]Egress, error) {
	path, err := idPath(http.MethodGet, "/rooms/%s/egress", roomID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Egress []Egress `json:"egress"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Egress, nil
}

// StopEgress stops a live stream out.
func (c *Client) StopEgress(ctx context.Context, roomID, egressID string) (*Egress, error) {
	path, err := idPath(http.MethodDelete, "/rooms/%s/egress/%s", roomID, egressID)
	if err != nil {
		return nil, err
	}
	var out Egress
	if err := c.do(ctx, http.MethodDelete, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AddEgressDestination adds a simulcast destination while live (Cloudflare Stream only).
func (c *Client) AddEgressDestination(ctx context.Context, roomID, egressID string, d EgressDestination) (*EgressDestination, error) {
	path, err := idPath(http.MethodPost, "/rooms/%s/egress/%s/destinations", roomID, egressID)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(d)
	var out EgressDestination
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveEgressDestination removes a simulcast destination.
func (c *Client) RemoveEgressDestination(ctx context.Context, roomID, egressID, destID string) error {
	path, err := idPath(http.MethodDelete, "/rooms/%s/egress/%s/destinations/%s", roomID, egressID, destID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// UsageQuery filters GET /usage. Zero values use the server defaults
// (7 days, grouped by day, all customers and rooms).
type UsageQuery struct {
	Days     int    // 1-90
	GroupBy  string // "day", "hour", "room", "customer", "user" or "event"
	Customer string
	RoomID   string
	Event    string // e.g. "participant"
	Limit    int    // 1-1000
}

// UsageRow is one group of usage data points.
type UsageRow struct {
	Key     string  `json:"key"`
	Event   string  `json:"event"`
	Count   int64   `json:"count"`
	Seconds int64   `json:"seconds"`
	Minutes float64 `json:"minutes"`
	Bytes   int64   `json:"bytes"`
}

// Usage reads usage from Workers Analytics Engine (needs the deployment's
// USAGE binding plus CF_ACCOUNT_ID / CF_ANALYTICS_TOKEN secrets).
func (c *Client) Usage(ctx context.Context, q UsageQuery) ([]UsageRow, error) {
	v := url.Values{}
	if q.Days > 0 {
		v.Set("days", strconv.Itoa(q.Days))
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	for k, s := range map[string]string{"groupBy": q.GroupBy, "customer": q.Customer, "roomId": q.RoomID, "event": q.Event} {
		if s != "" {
			v.Set(k, s)
		}
	}
	var out struct {
		Rows []UsageRow `json:"rows"`
	}
	path := "/usage"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Rows, nil
}

// PollInput starts a poll (2-10 options).
type PollInput struct {
	Question    string   `json:"question"`
	Options     []string `json:"options"`
	Multiple    bool     `json:"multiple,omitempty"`
	Anonymous   bool     `json:"anonymous,omitempty"`
	HideResults bool     `json:"hideResults,omitempty"`
}

// Poll with results. Votes/VoterNames are empty for anonymous polls.
type Poll struct {
	ID          string            `json:"id"`
	Question    string            `json:"question"`
	Options     []string          `json:"options"`
	Multiple    bool              `json:"multiple"`
	Anonymous   bool              `json:"anonymous"`
	HideResults bool              `json:"hideResults"`
	Counts      []int             `json:"counts"`
	Voters      int               `json:"voters"`
	Closed      bool              `json:"closed"`
	CreatedBy   string            `json:"createdBy"`
	At          int64             `json:"at"`
	Votes       map[string][]int  `json:"votes,omitempty"`
	VoterNames  map[string]string `json:"voterNames,omitempty"`
}

// Question asked in a room's Q&A.
type Question struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	UserID    string `json:"userId"`
	From      string `json:"from"`
	Anonymous bool   `json:"anonymous"`
	Upvotes   int    `json:"upvotes"`
	Answered  bool   `json:"answered"`
	At        int64  `json:"at"`
}

// CreatePoll starts a poll in a room; everyone connected sees it at once.
func (c *Client) CreatePoll(ctx context.Context, roomID string, in PollInput) (*Poll, error) {
	path, err := idPath(http.MethodPost, "/rooms/%s/polls", roomID)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	var out Poll
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PhonePin is a meeting code that phone callers type to reach a room.
type PhonePin struct {
	Pin       string `json:"pin"`
	RoomID    string `json:"roomId"`
	ExpiresAt int64  `json:"expiresAt"` // ms since epoch
	Label     string `json:"label,omitempty"`
}

// PhonePinRequest creates a meeting code. Pin empty: a random 8-digit code.
// TTLSeconds 0: 7 days (max 90 days). Label (up to 24 characters) is the
// caller's name in the room, instead of "Phone ••• 1234".
type PhonePinRequest struct {
	RoomID     string `json:"roomId"`
	Pin        string `json:"pin,omitempty"`
	TTLSeconds int    `json:"ttlSeconds,omitempty"`
	Label      string `json:"label,omitempty"`
}

// CreatePhonePin makes a dial-in code for a room (see "Phone dial-in" in the README).
func (c *Client) CreatePhonePin(ctx context.Context, req PhonePinRequest) (*PhonePin, error) {
	b, _ := json.Marshal(req)
	var out PhonePin
	if err := c.do(ctx, http.MethodPost, "/phone/pins", b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListPolls returns a room's polls with full results.
func (c *Client) ListPolls(ctx context.Context, roomID string) ([]Poll, error) {
	path, err := idPath(http.MethodGet, "/rooms/%s/polls", roomID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Polls []Poll `json:"polls"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Polls, nil
}

// ClosePoll stops voting and shows results to everyone.
func (c *Client) ClosePoll(ctx context.Context, roomID, pollID string) error {
	path, err := idPath(http.MethodPost, "/rooms/%s/polls/%s/close", roomID, pollID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, path, nil, nil)
}

// ListQuestions returns a room's Q&A questions (askers included, even anonymous ones).
func (c *Client) ListQuestions(ctx context.Context, roomID string) ([]Question, error) {
	path, err := idPath(http.MethodGet, "/rooms/%s/questions", roomID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Questions []Question `json:"questions"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Questions, nil
}

// TranscriptLine is one caption phrase.
type TranscriptLine struct {
	Seq      int     `json:"seq"`
	UserID   string  `json:"userId"`
	Name     string  `json:"name"`
	Text     string  `json:"text"`
	At       int64   `json:"at"`
	Duration float64 `json:"duration"`
}

// CallSummary is an AI summary of a call's transcript.
type CallSummary struct {
	Text  string `json:"text"`
	At    int64  `json:"at"`
	Lines int    `json:"lines"`
}

// SetCaptions turns live captions on or off for a room (needs Workers AI on the
// Worker). Language is an optional ISO 639-1 hint; translate gives English captions.
func (c *Client) SetCaptions(ctx context.Context, roomID string, on bool, language string, translate bool) error {
	path, err := idPath(http.MethodPut, "/rooms/%s/captions", roomID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"on": on, "language": language, "translate": translate})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, path, body, nil)
}

// Transcript returns a room's caption lines so far (kept until the room has been
// empty for 2 minutes; then archived to R2 if the Worker has a STORAGE bucket).
func (c *Client) Transcript(ctx context.Context, roomID string) ([]TranscriptLine, error) {
	path, err := idPath(http.MethodGet, "/rooms/%s/transcript", roomID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Lines []TranscriptLine `json:"lines"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Lines, nil
}

// Summarize asks Workers AI to summarize the transcript so far. Style is
// "meeting" or "lesson". Everyone in the room gets the summary too.
func (c *Client) Summarize(ctx context.Context, roomID, style string) (*CallSummary, error) {
	path, err := idPath(http.MethodPost, "/rooms/%s/summary", roomID)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]string{"style": style})
	if err != nil {
		return nil, err
	}
	var out CallSummary
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TenantLimits caps one customer's use of the kit. Nil fields = no limit.
type TenantLimits struct {
	MaxParticipantsPerRoom     *int     `json:"maxParticipantsPerRoom,omitempty"`
	ParticipantMinutesPerMonth *float64 `json:"participantMinutesPerMonth,omitempty"`
	CaptionMinutesPerMonth     *float64 `json:"captionMinutesPerMonth,omitempty"`
	FileMBPerMonth             *float64 `json:"fileMBPerMonth,omitempty"`
	Captions                   *bool    `json:"captions,omitempty"`
	Files                      *bool    `json:"files,omitempty"`
}

// Tenant is one customer with its own API key.
type Tenant struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Limits    TenantLimits `json:"limits"`
	Disabled  bool         `json:"disabled"`
	KeyPrefix string       `json:"keyPrefix"`
	CreatedAt int64        `json:"createdAt"`
	// WebhookURL is where the tenant's rooms send webhooks (nil = none).
	WebhookURL *string `json:"webhookUrl"`
}

// TenantKey is one of a tenant's API keys. The secret is only returned by CreateTenantKey.
type TenantKey struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt *int64 `json:"lastUsedAt"`
}

// TenantWebhookInfo is a tenant's webhook URL and signing secret (both empty when off).
type TenantWebhookInfo struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
}

// WebhookTestResult says whether a test delivery got a 2xx answer, and the HTTP status (0 = no answer).
type WebhookTestResult struct {
	Delivered bool `json:"delivered"`
	Status    int  `json:"status"`
}

// tenantPath is "/tenants/<id>"+rest with the master key, or rest alone for the
// calling tenant's own key (tenantID ""). The ids fill rest's %s; segPath escapes
// and checks them and tenantID.
func tenantPath(method, tenantID, rest string, ids ...string) (string, error) {
	if tenantID == "" {
		return segPath(method, rest, ids...)
	}
	return segPath(method, "/tenants/%s"+rest, append([]string{tenantID}, ids...)...)
}

// TenantUsage is one calendar month (UTC) of usage.
type TenantUsage struct {
	Period             string  `json:"period"`
	ParticipantMinutes float64 `json:"participantMinutes"`
	CaptionMinutes     float64 `json:"captionMinutes"`
	FileMB             float64 `json:"fileMB"`
	// Billing meters.
	RecordingMinutes      float64 `json:"recordingMinutes"`
	VideoRecordingMinutes float64 `json:"videoRecordingMinutes"`
	IngestMinutes         float64 `json:"ingestMinutes"`
	StreamOutMinutes      float64 `json:"streamOutMinutes"`
	AgentMinutes          float64 `json:"agentMinutes"`
}

// TenantPeriodUsage is one tenant's usage in one month ("_default" = your own rooms).
type TenantPeriodUsage struct {
	Tenant string `json:"tenant"`
	TenantUsage
}

// TenantsUsage returns every tenant's metered usage for one month ("YYYY-MM";
// "" = this month), for billing. Master key only.
func (c *Client) TenantsUsage(ctx context.Context, period string) ([]TenantPeriodUsage, error) {
	path := "/usage/tenants"
	if period != "" {
		path += "?period=" + url.QueryEscape(period)
	}
	var out struct {
		Tenants []TenantPeriodUsage `json:"tenants"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Tenants, nil
}

// CreateTenant adds a customer (master ADMIN_API_KEY only). The returned API key
// is shown only once: give it to that customer's backend, which then uses
// NewClient(url, apiKey) like any admin client, limited to its own rooms.
func (c *Client) CreateTenant(ctx context.Context, id, name string, limits TenantLimits) (*Tenant, string, error) {
	body, err := json.Marshal(map[string]any{"id": id, "name": name, "limits": limits})
	if err != nil {
		return nil, "", err
	}
	var out struct {
		Tenant Tenant `json:"tenant"`
		APIKey string `json:"apiKey"`
	}
	if err := c.do(ctx, http.MethodPost, "/tenants", body, &out); err != nil {
		return nil, "", err
	}
	return &out.Tenant, out.APIKey, nil
}

// UpdateTenant changes a tenant's name, limits (set fields only) or disabled flag.
func (c *Client) UpdateTenant(ctx context.Context, id string, name string, limits *TenantLimits, disabled *bool) (*Tenant, error) {
	path, err := idPath(http.MethodPatch, "/tenants/%s", id)
	if err != nil {
		return nil, err
	}
	patch := map[string]any{}
	if name != "" {
		patch["name"] = name
	}
	if limits != nil {
		patch["limits"] = limits
	}
	if disabled != nil {
		patch["disabled"] = *disabled
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tenant Tenant `json:"tenant"`
	}
	if err := c.do(ctx, http.MethodPatch, path, body, &out); err != nil {
		return nil, err
	}
	return &out.Tenant, nil
}

// RotateTenantKey replaces the tenant's "default" API key; the old one stops working.
// Keys made with CreateTenantKey are kept.
func (c *Client) RotateTenantKey(ctx context.Context, id string) (string, error) {
	path, err := idPath(http.MethodPost, "/tenants/%s/rotate-key", id)
	if err != nil {
		return "", err
	}
	var out struct {
		APIKey string `json:"apiKey"`
	}
	if err := c.do(ctx, http.MethodPost, path, nil, &out); err != nil {
		return "", err
	}
	return out.APIKey, nil
}

// ListTenantKeys lists a tenant's API keys (no secrets). tenantID "" = the calling tenant's own keys.
func (c *Client) ListTenantKeys(ctx context.Context, tenantID string) ([]TenantKey, error) {
	var out struct {
		Keys []TenantKey `json:"keys"`
	}
	path, err := tenantPath(http.MethodGet, tenantID, "/keys")
	if err != nil {
		return nil, err
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

// CreateTenantKey adds a named API key; the secret is returned only here.
// tenantID "" = a key for the calling tenant.
func (c *Client) CreateTenantKey(ctx context.Context, tenantID, name string) (*TenantKey, string, error) {
	body, err := json.Marshal(map[string]any{"name": name})
	if err != nil {
		return nil, "", err
	}
	var out struct {
		Key    TenantKey `json:"key"`
		APIKey string    `json:"apiKey"`
	}
	path, err := tenantPath(http.MethodPost, tenantID, "/keys")
	if err != nil {
		return nil, "", err
	}
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, "", err
	}
	return &out.Key, out.APIKey, nil
}

// DeleteTenantKey deletes one key; it stops working within 15 seconds. A tenant
// can't delete its own last key (409).
func (c *Client) DeleteTenantKey(ctx context.Context, tenantID, keyID string) error {
	path, err := tenantPath(http.MethodDelete, tenantID, "/keys/%s", keyID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// SetTenantWebhook sets where the tenant's rooms send webhooks (https). webhookURL ""
// turns them off. A signing secret is made the first time; read it with TenantWebhook.
func (c *Client) SetTenantWebhook(ctx context.Context, tenantID, webhookURL string) error {
	var v any
	if webhookURL != "" {
		v = webhookURL
	}
	if tenantID == "" {
		body, err := json.Marshal(map[string]any{"url": v})
		if err != nil {
			return err
		}
		return c.do(ctx, http.MethodPut, "/webhook", body, nil)
	}
	body, err := json.Marshal(map[string]any{"webhookUrl": v})
	if err != nil {
		return err
	}
	path, err := segPath(http.MethodPatch, "/tenants/%s", tenantID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPatch, path, body, nil)
}

// TenantWebhook returns the tenant's webhook URL and the secret its deliveries
// are signed with (check them with VerifyWebhook).
func (c *Client) TenantWebhook(ctx context.Context, tenantID string) (*TenantWebhookInfo, error) {
	var out struct {
		URL    *string `json:"url"`
		Secret *string `json:"secret"`
	}
	path, err := tenantPath(http.MethodGet, tenantID, "/webhook")
	if err != nil {
		return nil, err
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	info := &TenantWebhookInfo{}
	if out.URL != nil {
		info.URL = *out.URL
	}
	if out.Secret != nil {
		info.Secret = *out.Secret
	}
	return info, nil
}

// RotateTenantWebhookSecret makes a new signing secret; deliveries use it at once.
func (c *Client) RotateTenantWebhookSecret(ctx context.Context, tenantID string) (string, error) {
	var out struct {
		Secret string `json:"secret"`
	}
	path, err := tenantPath(http.MethodPost, tenantID, "/webhook/rotate-secret")
	if err != nil {
		return "", err
	}
	if err := c.do(ctx, http.MethodPost, path, nil, &out); err != nil {
		return "", err
	}
	return out.Secret, nil
}

// TestTenantWebhook sends a "webhook_test" event to the tenant's webhook now.
func (c *Client) TestTenantWebhook(ctx context.Context, tenantID string) (*WebhookTestResult, error) {
	var out WebhookTestResult
	path, err := tenantPath(http.MethodPost, tenantID, "/webhook/test")
	if err != nil {
		return nil, err
	}
	if err := c.do(ctx, http.MethodPost, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteTenant removes a tenant and its keys.
func (c *Client) DeleteTenant(ctx context.Context, id string) error {
	path, err := idPath(http.MethodDelete, "/tenants/%s", id)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// MonthlyUsage returns the calling key's metered usage (the counters its limits
// are checked against), newest month first. GET /usage/limits.
func (c *Client) MonthlyUsage(ctx context.Context) ([]TenantUsage, error) {
	var out struct {
		Usage []TenantUsage `json:"usage"`
	}
	if err := c.do(ctx, http.MethodGet, "/usage/limits", nil, &out); err != nil {
		return nil, err
	}
	return out.Usage, nil
}

// PhonePins lists the live codes, for one room or (roomID "") all rooms.
func (c *Client) PhonePins(ctx context.Context, roomID string) ([]PhonePin, error) {
	path := "/phone/pins"
	if roomID != "" {
		path += "?roomId=" + url.QueryEscape(roomID)
	}
	var out struct {
		Pins []PhonePin `json:"pins"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Pins, nil
}

// DeletePhonePin stops a code from working. Calls already in the room stay.
func (c *Client) DeletePhonePin(ctx context.Context, pin string) error {
	path, err := segPath(http.MethodDelete, "/phone/pins/%s", pin)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// idPath fills each %s in format with the matching id escaped as one path
// segment, so an id can't point the admin request at another route. An id the
// kit refuses (idPattern), or "." or ".." (which URL normalization resolves),
// fails with a 400 *APIError for method before any request is sent.
func idPath(method, format string, ids ...string) (string, error) {
	return escapeIDs(method, format, true, ids)
}

// segPath is idPath for ids with formats of their own (lesson plans, timetable
// runs, tenant keys, phone pins, ...): any id but "", "." and "..", which
// url.PathEscape leaves as they are.
func segPath(method, format string, ids ...string) (string, error) {
	return escapeIDs(method, format, false, ids)
}

func escapeIDs(method, format string, strict bool, ids []string) (string, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = url.PathEscape(id)
	}
	path := fmt.Sprintf(format, args...)
	for _, id := range ids {
		if id == "" || id == "." || id == ".." || strict && !idPattern.MatchString(id) {
			return "", &APIError{Method: method, Path: path, Status: http.StatusBadRequest, Body: fmt.Sprintf("invalid id %q", id)}
		}
	}
	return path, nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.AdminKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return &APIError{Method: method, Path: path, Status: res.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// APIError is a non-2xx answer from the kit. Use errors.As to read Status
// (401 wrong admin key, 404 unknown route, 400 bad input, ...).
type APIError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("sfukit %s %s: %d %s", e.Method, e.Path, e.Status, e.Body)
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9._:@-]{1,128}$`)
var rolePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// SignToken creates a join token locally with the deployment's JWT_SECRET.
// It matches what the kit's POST /tokens returns.
func SignToken(jwtSecret string, req TokenRequest) (*Token, error) {
	if len(jwtSecret) < 32 {
		return nil, errors.New("jwtSecret must be at least 32 characters")
	}
	if !idPattern.MatchString(req.RoomID) || !idPattern.MatchString(req.UserID) {
		return nil, errors.New("roomId and userId must be 1-128 chars of letters, numbers, . _ : @ -")
	}
	ttl := req.TTLSeconds
	if ttl == 0 {
		ttl = 7200
	}
	if ttl < 60 {
		ttl = 60
	}
	if ttl > 86400 {
		ttl = 86400
	}
	name := req.Name
	if name == "" {
		name = req.UserID
	}
	if r := []rune(name); len(r) > 80 {
		name = string(r[:80])
	}
	if req.Role != "" && !rolePattern.MatchString(req.Role) {
		return nil, errors.New("role must be 1-32 chars of letters, numbers, . _ -")
	}
	pub := true
	if req.CanPublish != nil {
		pub = *req.CanPublish
	}
	if req.MaxParticipants != 0 && (req.MaxParticipants < 2 || req.MaxParticipants > 10000) {
		return nil, errors.New("maxParticipants must be 2-10000")
	}
	switch req.DataSaver {
	case "", "off", "low", "minimal", "audio":
	default:
		return nil, errors.New(`dataSaver must be "off", "low", "minimal" or "audio"`)
	}
	perm, builtin := RolePermissions[req.Role]
	if !builtin {
		perm = Permissions{Mic: pub, Camera: pub, Screen: pub}
	}
	perm = perm.apply(req.Permissions)
	pub = perm.Mic || perm.Camera || perm.Screen
	now := time.Now().Unix()
	exp := now + int64(ttl)

	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	claims := map[string]any{
		"sub":  req.UserID,
		"room": req.RoomID,
		"name": name,
		"pub":  pub,
		"perm": perm,
		"aud":  "sfukit",
		"iat":  now,
		"exp":  exp,
	}
	if req.MaxParticipants != 0 {
		claims["max"] = req.MaxParticipants
	}
	if req.Role != "" {
		claims["role"] = req.Role
	}
	if req.Metadata != nil {
		md, err := json.Marshal(req.Metadata)
		if err != nil || len(md) > maxMetadataBytes {
			return nil, errors.New("metadata must be JSON of at most 4 KB")
		}
		claims["md"] = req.Metadata
	}
	if req.E2EE {
		claims["e2ee"] = true
	}
	if req.DataSaver != "" && req.DataSaver != "off" {
		claims["ds"] = req.DataSaver
	}
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(h) + "." + enc.EncodeToString(p)
	mac := hmac.New(sha256.New, []byte(jwtSecret))
	mac.Write([]byte(signingInput))
	return &Token{
		Token:     signingInput + "." + enc.EncodeToString(mac.Sum(nil)),
		ExpiresAt: exp,
	}, nil
}

// Bool is a helper for TokenRequest.CanPublish.
func Bool(v bool) *bool { return &v }

// WebhookEvent is what the kit POSTs to your WEBHOOK_URL.
type WebhookEvent struct {
	ID        string `json:"id"`
	Event     string `json:"event"` // room_started, room_finished, participant_joined, participant_left, track_published, track_unpublished
	RoomID    string `json:"roomId"`
	CreatedAt int64  `json:"createdAt"`
	// Seq increases by one per event in a room session; deliveries can arrive out of order.
	Seq         int `json:"seq"`
	Participant *struct {
		ID       string         `json:"id"`
		UserID   string         `json:"userId"`
		Name     string         `json:"name"`
		Role     string         `json:"role"`
		Metadata map[string]any `json:"metadata,omitempty"`
	} `json:"participant,omitempty"`
	Track *struct {
		Kind      string `json:"kind"`
		TrackName string `json:"trackName"`
		SessionID string `json:"sessionId"`
	} `json:"track,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// VerifyWebhook checks the X-SfuKit-Signature header ("t=<unix>,v1=<hex hmac>")
// against the raw request body and returns the event. secret is the
// deployment's WEBHOOK_SECRET (or ADMIN_API_KEY if none is set). Signatures
// older than 5 minutes are rejected.
func VerifyWebhook(secret string, body []byte, signatureHeader string) (*WebhookEvent, error) {
	var ts, sig string
	for _, part := range strings.Split(signatureHeader, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || sig == "" || secret == "" {
		return nil, errors.New("sfukit: malformed webhook signature")
	}
	if d := time.Now().Unix() - t; d > 300 || d < -300 {
		return nil, errors.New("sfukit: webhook signature too old")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(body)))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return nil, errors.New("sfukit: bad webhook signature")
	}
	var ev WebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, err
	}
	return &ev, nil
}

// SignWebhook builds an X-SfuKit-Signature header for body at unix time t
// (for tests of your webhook endpoint).
func SignWebhook(secret string, body []byte, t int64) string {
	ts := strconv.FormatInt(t, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(body)))
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
