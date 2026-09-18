package max

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func newTestBot(t *testing.T, handler http.HandlerFunc) *Bot {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	origBase := apiBase
	apiBase = ts.URL
	t.Cleanup(func() { apiBase = origBase })
	b := New("test-token")
	b.client = ts.Client()
	return b
}

func TestSendFileUploadsAndRetriesNotReady(t *testing.T) {
	var messages atomic.Int32
	b := newTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/uploads":
			if r.Header.Get("Authorization") != "test-token" {
				t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			}
			if r.URL.Query().Get("type") != "image" {
				t.Errorf("upload type = %q, want image", r.URL.Query().Get("type"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"url":"`+apiBase+`/upload-data","token":"image-token"}`)
		case "/upload-data":
			if r.Method != http.MethodPost {
				t.Errorf("upload method = %s", r.Method)
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("ParseMultipartForm: %v", err)
			}
			file, _, err := r.FormFile("data")
			if err != nil {
				t.Errorf("data part: %v", err)
			} else {
				defer file.Close()
				data, _ := io.ReadAll(file)
				if string(data) != "png-bytes" {
					t.Errorf("upload data = %q", data)
				}
			}
			w.WriteHeader(http.StatusOK)
		case "/messages":
			if r.Header.Get("Authorization") != "test-token" {
				t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			}
			if r.URL.Query().Get("user_id") != "42" {
				t.Errorf("user_id = %q", r.URL.Query().Get("user_id"))
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"image-token"`) || !strings.Contains(string(body), `"mid-1"`) {
				t.Errorf("message body = %s", body)
			}
			if messages.Add(1) == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"code":"attachment.not.ready"}`)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})

	if err := b.SendFile("42", "chart.png", "image/png", []byte("png-bytes"), "chart", "mid-1"); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if got := messages.Load(); got != 2 {
		t.Fatalf("message attempts = %d, want 2", got)
	}
}

func TestSendFileDoesNotRetryPermanentMessageError(t *testing.T) {
	var messages atomic.Int32
	b := newTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/uploads":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"url":"`+apiBase+`/upload-data","token":"file-token"}`)
		case "/upload-data":
			w.WriteHeader(http.StatusOK)
		case "/messages":
			messages.Add(1)
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"code":"forbidden"}`)
		default:
			http.NotFound(w, r)
		}
	})

	if err := b.SendFile("-9", "file.txt", "text/plain", []byte("data"), "", ""); err == nil {
		t.Fatal("SendFile unexpectedly succeeded")
	}
	if got := messages.Load(); got != 1 {
		t.Fatalf("message attempts = %d, want 1", got)
	}
}
