package sfukit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
)

// Videos (video on demand on R2): uploads go straight from the browser to the
// upload link, Appsocom Live turns them into HLS, and viewers play with signed links.
// Docs: https://docs.appsocom.dev/videos/

// Video is one video in the project's library.
type Video struct {
	VideoID string `json:"videoId"`
	Name    string `json:"name"`
	// Status is "waiting" (upload not finished), "processing", "ready" or "failed".
	Status          string            `json:"status"`
	Progress        *int              `json:"progress"`
	Error           *string           `json:"error"`
	DurationSeconds *float64          `json:"durationSeconds"`
	SizeBytes       *int64            `json:"sizeBytes"`
	Width           *int              `json:"width"`
	Height          *int              `json:"height"`
	Renditions      []string          `json:"renditions"`
	Meta            map[string]string `json:"meta"`
	CreatedAt       *string           `json:"createdAt"`
	UploadedAt      *string           `json:"uploadedAt"`
}

// VideoUploadRequest asks for an upload link.
type VideoUploadRequest struct {
	// MaxDurationSeconds is the longest the video may be; longer videos are refused when converted.
	MaxDurationSeconds int               `json:"maxDurationSeconds"`
	Name               string            `json:"name,omitempty"`
	Meta               map[string]string `json:"meta,omitempty"`
	// UploadLength is the file size in bytes. Over 95 MB it must be given, and the
	// upload goes in parts (up to 5 GB).
	UploadLength int64 `json:"uploadLength,omitempty"`
}

// VideoUpload is where the browser sends the file.
type VideoUpload struct {
	VideoID   string `json:"videoId"`
	UploadURL string `json:"uploadUrl"`
	// Protocol is "put" (PUT the whole file to UploadURL) or "parts" (PUT each
	// PartSize slice to UploadURL+"&part=N", then POST UploadURL+"&complete"
	// with {"parts":[{"partNumber","etag"}]}).
	Protocol  string `json:"protocol"`
	PartSize  int64  `json:"partSize,omitempty"`
	Parts     int    `json:"parts,omitempty"`
	ExpiresAt string `json:"expiresAt"`
}

// VideoPlayback holds signed links for one viewer.
type VideoPlayback struct {
	VideoID   string `json:"videoId"`
	HLS       string `json:"hls"`
	Iframe    string `json:"iframe"`
	Thumbnail string `json:"thumbnail"`
	ExpiresAt string `json:"expiresAt"`
}

// VideoUpdate changes a video; nil fields are left as they are.
type VideoUpdate struct {
	Name *string           `json:"name,omitempty"`
	Meta map[string]string `json:"meta,omitempty"`
}

// VideoUsage is the project's storage, this month's meters and its caps.
type VideoUsage struct {
	StorageBytes int64   `json:"storageBytes"`
	Videos       int     `json:"videos"`
	Period       *string `json:"period"`
	// StorageGBMonths is this month's storage so far (counted daily).
	StorageGBMonths  *float64 `json:"storageGBMonths"`
	ProcessedMinutes *float64 `json:"processedMinutes"`
	Limits           struct {
		Videos                        bool     `json:"videos"`
		VideoStorageGB                *float64 `json:"videoStorageGB"`
		VideoProcessedMinutesPerMonth *float64 `json:"videoProcessedMinutesPerMonth"`
		MaxDurationSeconds            int      `json:"maxDurationSeconds"`
	} `json:"limits"`
}

var videoIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func videoPath(method, videoID, suffix string) (string, error) {
	if !videoIDPattern.MatchString(videoID) {
		return "", &APIError{Method: method, Path: "/videos/" + url.PathEscape(videoID) + suffix, Status: http.StatusBadRequest, Body: "invalid video id"}
	}
	return "/videos/" + videoID + suffix, nil
}

func (c *Client) videoCall(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	return c.do(ctx, method, path, body, out)
}

// CreateVideoUpload returns an upload link for one video. Give UploadURL to the browser.
func (c *Client) CreateVideoUpload(ctx context.Context, req VideoUploadRequest) (*VideoUpload, error) {
	var out VideoUpload
	if err := c.videoCall(ctx, http.MethodPost, "/videos/uploads", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CopyVideo brings in a video from a public https link.
func (c *Client) CopyVideo(ctx context.Context, link, name string, meta map[string]string) (*Video, error) {
	var out Video
	in := map[string]any{"url": link}
	if name != "" {
		in["name"] = name
	}
	if meta != nil {
		in["meta"] = meta
	}
	if err := c.videoCall(ctx, http.MethodPost, "/videos/copy", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListVideos returns the project's videos, newest first. Pass the returned
// next (non-empty) as after for the next page; limit 0 means 50.
func (c *Client) ListVideos(ctx context.Context, after string, limit int) (videos []Video, next string, err error) {
	q := url.Values{}
	if after != "" {
		q.Set("after", after)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/videos"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Videos []Video `json:"videos"`
		Next   *string `json:"next"`
	}
	if err := c.videoCall(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, "", err
	}
	if out.Next != nil {
		next = *out.Next
	}
	return out.Videos, next, nil
}

// GetVideo returns one video (its status while it processes).
func (c *Client) GetVideo(ctx context.Context, videoID string) (*Video, error) {
	path, err := videoPath(http.MethodGet, videoID, "")
	if err != nil {
		return nil, err
	}
	var out Video
	if err := c.videoCall(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateVideo renames a video or replaces its meta.
func (c *Client) UpdateVideo(ctx context.Context, videoID string, update VideoUpdate) (*Video, error) {
	path, err := videoPath(http.MethodPatch, videoID, "")
	if err != nil {
		return nil, err
	}
	var out Video
	if err := c.videoCall(ctx, http.MethodPatch, path, update, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteVideo deletes a video for good.
func (c *Client) DeleteVideo(ctx context.Context, videoID string) error {
	path, err := videoPath(http.MethodDelete, videoID, "")
	if err != nil {
		return err
	}
	return c.videoCall(ctx, http.MethodDelete, path, nil, nil)
}

// VideoPlayback returns signed links for one viewer. expiresInSeconds 0 means
// long enough to watch the video twice (at least an hour).
func (c *Client) VideoPlayback(ctx context.Context, videoID string, expiresInSeconds int) (*VideoPlayback, error) {
	path, err := videoPath(http.MethodPost, videoID, "/playback")
	if err != nil {
		return nil, err
	}
	in := map[string]any{}
	if expiresInSeconds > 0 {
		in["expiresInSeconds"] = expiresInSeconds
	}
	var out VideoPlayback
	if err := c.videoCall(ctx, http.MethodPost, path, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// VideoUsage returns bytes kept, this month's meters and the project's caps.
func (c *Client) VideoUsage(ctx context.Context) (*VideoUsage, error) {
	var out VideoUsage
	if err := c.videoCall(ctx, http.MethodGet, "/videos/usage", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
