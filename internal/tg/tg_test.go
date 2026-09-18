package tg

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestBot starts an httptest.Server, points the package-level apiBase at
// it (restored via t.Cleanup), and returns a Bot wired to it.
func newTestBot(t *testing.T, handler http.HandlerFunc) *Bot {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	origBase := apiBase
	apiBase = ts.URL + "/bot"
	t.Cleanup(func() { apiBase = origBase })

	b := New("test-token")
	b.client = ts.Client()
	return b
}

func TestGetMeDecodesIdentity(t *testing.T) {
	var gotPath string
	b := newTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":123456,"is_bot":true,"first_name":"klax","username":"klax_dev_bot"}}`))
	})

	me, err := b.GetMe()
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if me.ID != 123456 || me.Username != "klax_dev_bot" {
		t.Fatalf("GetMe = %+v, want id=123456 username=klax_dev_bot", me)
	}
	if gotPath != "/bottest-token/getMe" {
		t.Fatalf("request path = %q, want /bottest-token/getMe", gotPath)
	}
}

func TestGetMeReturnsAPIErrorWhenNotOK(t *testing.T) {
	b := newTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
	})

	_, err := b.GetMe()

	if err == nil {
		t.Fatal("expected an error for ok=false")
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Code != 401 {
		t.Fatalf("err = %v, want *APIError with code 401", err)
	}
}

func TestSendFileUploadsDocumentAndReply(t *testing.T) {
	b := newTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottest-token/sendDocument" {
			t.Fatalf("path = %q, want sendDocument", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		file, hdr, err := r.FormFile("document")
		if err != nil {
			t.Fatalf("document part: %v", err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Filename != "report.pdf" || string(data) != "payload" {
			t.Fatalf("uploaded file = %q/%q, want report.pdf/payload", hdr.Filename, data)
		}
		if got := r.FormValue("chat_id"); got != "123" {
			t.Errorf("chat_id = %q, want 123", got)
		}
		var reply struct {
			MessageID int `json:"message_id"`
		}
		if err := json.Unmarshal([]byte(r.FormValue("reply_parameters")), &reply); err != nil {
			t.Fatalf("reply_parameters: %v", err)
		}
		if reply.MessageID != 7 || r.FormValue("caption") != "result" {
			t.Fatalf("reply/caption = %d/%q, want 7/result", reply.MessageID, r.FormValue("caption"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":8}}`)
	})

	if err := b.SendFile("123", "report.pdf", "application/pdf", []byte("payload"), "result", "7"); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
}
