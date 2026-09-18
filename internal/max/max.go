// Package max provides a minimal MAX (max.ru) Bot API client.
// Uses long-polling for updates and REST for sending messages.
package max

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PiDmitrius/klax/internal/transport"
)

const apiBase = "https://platform-api2.max.ru"

type Bot struct {
	token  string
	client *http.Client
	marker *int64
}

func New(token string) *Bot {
	return &Bot{
		token:  token,
		client: newMaxHTTPClient(),
	}
}

// --- Types ---

type User struct {
	UserID   int64  `json:"user_id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

type Recipient struct {
	ChatID   int64  `json:"chat_id,omitempty"`
	ChatType string `json:"chat_type,omitempty"`
	UserID   int64  `json:"user_id,omitempty"`
}

type MessageBody struct {
	Mid            string            `json:"mid"`
	Seq            int64             `json:"seq"`
	Text           string            `json:"text"`
	RawAttachments []json.RawMessage `json:"attachments"`
}

// AttachmentType identifies the kind of attachment.
type AttachmentType struct {
	Type string `json:"type"`
}

type PhotoAttachment struct {
	AttachmentType
	Payload struct {
		URL string `json:"url"`
	} `json:"payload"`
}

type FileAttachment struct {
	AttachmentType
	Payload struct {
		URL string `json:"url"`
	} `json:"payload"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

// Attachment represents a parsed incoming attachment with a download URL and filename.
type Attachment struct {
	Type     string // "photo", "file", "video", "audio"
	URL      string
	Filename string
}

// ParseAttachments parses the raw attachments into typed Attachment structs.
func (mb *MessageBody) ParseAttachments() []Attachment {
	var result []Attachment
	for _, raw := range mb.RawAttachments {
		var base AttachmentType
		if json.Unmarshal(raw, &base) != nil {
			continue
		}
		switch base.Type {
		case "image":
			var a PhotoAttachment
			if json.Unmarshal(raw, &a) == nil && a.Payload.URL != "" {
				result = append(result, Attachment{Type: "photo", URL: a.Payload.URL, Filename: "photo.jpg"})
			}
		case "file":
			var a FileAttachment
			if json.Unmarshal(raw, &a) == nil && a.Payload.URL != "" {
				name := a.Filename
				if name == "" {
					name = "file"
				}
				result = append(result, Attachment{Type: "file", URL: a.Payload.URL, Filename: name})
			}
		case "video":
			var a struct {
				AttachmentType
				Payload struct {
					URL string `json:"url"`
				} `json:"payload"`
			}
			if json.Unmarshal(raw, &a) == nil && a.Payload.URL != "" {
				result = append(result, Attachment{Type: "video", URL: a.Payload.URL, Filename: "video.mp4"})
			}
		case "audio":
			var a struct {
				AttachmentType
				Payload struct {
					URL string `json:"url"`
				} `json:"payload"`
			}
			if json.Unmarshal(raw, &a) == nil && a.Payload.URL != "" {
				result = append(result, Attachment{Type: "audio", URL: a.Payload.URL, Filename: "audio.mp3"})
			}
		}
	}
	return result
}

type Message struct {
	Sender    User        `json:"sender"`
	Recipient Recipient   `json:"recipient"`
	Timestamp int64       `json:"timestamp"`
	Body      MessageBody `json:"body"`
}

type Update struct {
	UpdateType string  `json:"update_type"`
	Timestamp  int64   `json:"timestamp"`
	Message    Message `json:"message"`
}

type updatesResp struct {
	Updates []Update `json:"updates"`
	Marker  *int64   `json:"marker"`
}

type sentMessage struct {
	Message struct {
		Body struct {
			Mid string `json:"mid"`
		} `json:"body"`
	} `json:"message"`
}

// --- API helpers ---

func httpError(code int, desc string) *transport.APIError {
	return &transport.APIError{
		Platform:    "max",
		Code:        code,
		Description: desc,
	}
}

func (b *Bot) request(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, apiBase+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", b.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return b.client.Do(req)
}

// GetMe validates the bot token.
func (b *Bot) GetMe() (*User, error) {
	resp, err := b.request("GET", "/me", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		data, _ := io.ReadAll(resp.Body)
		return nil, httpError(resp.StatusCode, "GET /me: "+string(data))
	}
	var u User
	return &u, json.NewDecoder(resp.Body).Decode(&u)
}

// GetUpdates performs a single long-poll call and returns new updates.
func (b *Bot) GetUpdates() ([]Update, error) {
	path := "/updates?timeout=30&types=message_created"
	if b.marker != nil {
		path += "&marker=" + strconv.FormatInt(*b.marker, 10)
	}
	resp, err := b.request("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		data, _ := io.ReadAll(resp.Body)
		return nil, httpError(resp.StatusCode, "GET /updates: "+string(data))
	}
	var r updatesResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	b.marker = r.Marker
	return r.Updates, nil
}

// DrainUpdates advances past all pending updates.
func (b *Bot) DrainUpdates() error {
	// Do a poll with timeout=0 to get current marker.
	path := "/updates?timeout=0&types=message_created"
	if b.marker != nil {
		path += "&marker=" + strconv.FormatInt(*b.marker, 10)
	}
	resp, err := b.request("GET", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r updatesResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}
	b.marker = r.Marker
	return nil
}

// SendMessage sends a text message. For DM use userID>0, chatID=0. For group use chatID.
func (b *Bot) SendMessage(chatID, text, replyTo, format string) error {
	_, err := b.sendMsg(chatID, text, replyTo, format)
	return err
}

// SendMessageReturnID sends a message and returns its mid.
func (b *Bot) SendMessageReturnID(chatID, text, replyTo, format string) (string, error) {
	return b.sendMsg(chatID, text, replyTo, format)
}

// SendFile uploads a local file and sends it as a MAX attachment. MAX requires
// a type-specific upload URL before the attachment token can be used in a
// message.
func (b *Bot) SendFile(chatID, name, contentType string, data []byte, caption, replyTo string) error {
	typ := "file"
	switch {
	case strings.HasPrefix(contentType, "image/"):
		typ = "image"
	case strings.HasPrefix(contentType, "video/"):
		typ = "video"
	case strings.HasPrefix(contentType, "audio/"):
		typ = "audio"
	}

	resp, err := b.request("POST", "/uploads?type="+typ, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return httpError(resp.StatusCode, "POST /uploads: "+string(raw))
	}
	var upload struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&upload); err != nil {
		return err
	}
	if upload.URL == "" {
		return fmt.Errorf("MAX upload response has no URL")
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="data"; filename=%q`, filepath.Base(name)))
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	uploadReq, err := http.NewRequest(http.MethodPost, upload.URL, &body)
	if err != nil {
		return err
	}
	uploadReq.Header.Set("Content-Type", mw.FormDataContentType())
	uploadResp, err := b.client.Do(uploadReq)
	if err != nil {
		return err
	}
	defer uploadResp.Body.Close()
	if uploadResp.StatusCode < 200 || uploadResp.StatusCode >= 300 {
		raw, _ := io.ReadAll(uploadResp.Body)
		return httpError(uploadResp.StatusCode, "upload: "+string(raw))
	}
	if upload.Token == "" {
		var result struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(uploadResp.Body).Decode(&result); err != nil {
			return err
		}
		upload.Token = result.Token
	}
	if upload.Token == "" {
		return fmt.Errorf("MAX upload response has no token")
	}

	payload := map[string]interface{}{"token": upload.Token}
	message := map[string]interface{}{
		"attachments": []interface{}{map[string]interface{}{"type": typ, "payload": payload}},
	}
	if caption != "" {
		message["text"] = caption
	}
	if replyTo != "" {
		message["link"] = map[string]string{"type": "reply", "mid": replyTo}
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 3; attempt++ {
		err := b.sendRawMessage(chatID, encoded)
		if err == nil {
			return nil
		}
		// MAX may need time to process a freshly uploaded attachment.
		// Retry only that transient condition; do not duplicate arbitrary
		// failed messages or hide permanent API errors.
		if !strings.Contains(err.Error(), "attachment.not.ready") || attempt == 2 {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
	}
	return nil
}

func (b *Bot) sendRawMessage(chatID string, body []byte) error {
	var query string
	if id, err := strconv.ParseInt(chatID, 10, 64); err == nil && id > 0 {
		query = "/messages?user_id=" + chatID
	} else {
		query = "/messages?chat_id=" + chatID
	}
	resp, err := b.request("POST", query, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return httpError(resp.StatusCode, "POST /messages: "+string(raw))
	}
	return nil
}

func (b *Bot) sendMsg(chatID, text, replyTo, format string) (string, error) {
	payload := map[string]interface{}{
		"text": text,
	}
	if format == "markdown" || format == "html" {
		payload["format"] = format
	}
	if replyTo != "" {
		payload["link"] = map[string]string{
			"type": "reply",
			"mid":  replyTo,
		}
	}
	body, _ := json.Marshal(payload)

	// Positive IDs are user IDs (DMs), negative are chat IDs (groups).
	var query string
	if id, err := strconv.ParseInt(chatID, 10, 64); err == nil && id > 0 {
		query = "/messages?user_id=" + chatID
	} else {
		query = "/messages?chat_id=" + chatID
	}
	resp, err := b.request("POST", query, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		data, _ := io.ReadAll(resp.Body)
		return "", httpError(resp.StatusCode, "POST /messages: "+string(data))
	}
	var msg sentMessage
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return "", err
	}
	return msg.Message.Body.Mid, nil
}

// DownloadURL downloads a file from a direct URL (from attachment payload).
func (b *Bot) DownloadURL(url string) ([]byte, error) {
	resp, err := b.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// EditMessage edits an existing message by mid. replyTo is unused — MAX's
// edit is a dedicated endpoint that never touches the reply relationship.
func (b *Bot) EditMessage(chatID, messageID, text, replyTo, format string) error {
	payload := map[string]interface{}{
		"text": text,
	}
	if format == "markdown" || format == "html" {
		payload["format"] = format
	}
	body, _ := json.Marshal(payload)
	resp, err := b.request("PUT", "/messages?message_id="+messageID, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		data, _ := io.ReadAll(resp.Body)
		return httpError(resp.StatusCode, "PUT /messages: "+string(data))
	}
	return nil
}
