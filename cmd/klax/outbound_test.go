package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PiDmitrius/klax/internal/config"
	"github.com/PiDmitrius/klax/internal/sessfiles"
	"github.com/PiDmitrius/klax/internal/session"
	"github.com/PiDmitrius/klax/internal/transport"
)

// rewriteOutboundForUI: an in-root file link/image becomes a capability URL (and is
// snapshotted into the durable store); an out-of-root link degrades to plain text
// (never a dead local path); a remote link is untouched.
func TestRewriteOutboundForUI(t *testing.T) {
	t.Setenv("KLAX_DATA_DIR", t.TempDir())
	cwd := t.TempDir()
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAIAAAABCAIAAAB7QOjdAAAAD0lEQVR4nGP8z8DAwMAAAAYIAQHLR3Z1AAAAAElFTkSuQmCC")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "chart.png"), png, 0600); err != nil {
		t.Fatal(err)
	}

	d := newTestDeliveryDaemon(&fakeTransport{})
	d.store = &session.Store{Chats: map[string]*session.ChatSessions{}, Scope: map[string]*session.ScopeDefaults{}}
	d.store.New("tg:1", "one", cwd, session.ScopeDefaults{})
	d.runners = make(map[runnerKey]*sessionRunner)
	d.uiHub = newUIHub() // UI on: the file-link rewrite is enabled
	klaxID := d.store.SessionsFor("tg:1")[0].KlaxID

	md := "img ![c](chart.png) esc [r](../../../etc/passwd) web [w](https://x.com/a)"
	out, published := d.rewriteOutboundForUI("tg:1", klaxID, 1, md)
	if published {
		t.Fatal("a degraded link was reported as published")
	}
	if _, published := d.rewriteOutboundForUI("tg:1", klaxID, 1, "img ![c](chart.png) web [w](https://x.com/a)"); !published {
		t.Fatal("published links reported as degraded")
	}

	if !strings.Contains(out, "![c](/api/file?ref=") {
		t.Fatalf("in-root image must be rewritten to a capability URL: %q", out)
	}
	if !strings.Contains(out, "&w=2&h=1)") {
		t.Fatalf("rewritten local image must carry dimensions: %q", out)
	}
	if strings.Contains(out, "passwd") {
		t.Fatalf("out-of-root link must degrade to its label: %q", out)
	}
	if !strings.Contains(out, "[w](https://x.com/a)") {
		t.Fatalf("remote link must be untouched: %q", out)
	}
	// The snapshot landed in the durable store as an out-* entry.
	filesDir := filepath.Dir(sessfiles.Open("tg:1", klaxID).Path("x"))
	ents, _ := os.ReadDir(filesDir)
	found := false
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "out-") {
			found = true
		}
	}
	if !found {
		t.Fatalf("outbound file was not snapshotted into %s", filesDir)
	}

	// With the UI off the markdown is returned unchanged.
	d.uiHub = nil
	if got, _ := d.rewriteOutboundForUI("tg:1", klaxID, 1, md); got != md {
		t.Fatalf("UI-off must pass through unchanged: %q", got)
	}
}

// A link keeps rendering after the agent deletes, renames or moves the original.
func TestRewriteOutboundSurvivesADeletedOriginal(t *testing.T) {
	t.Setenv("KLAX_DATA_DIR", t.TempDir())
	cwd := t.TempDir()
	src := filepath.Join(cwd, "report.csv")
	if err := os.WriteFile(src, []byte("a,b\n1,2\n"), 0600); err != nil {
		t.Fatal(err)
	}

	d := newTestDeliveryDaemon(&fakeTransport{})
	d.store = &session.Store{Chats: map[string]*session.ChatSessions{}, Scope: map[string]*session.ScopeDefaults{}}
	d.store.New("tg:1", "one", cwd, session.ScopeDefaults{})
	d.runners = make(map[runnerKey]*sessionRunner)
	d.uiHub = newUIHub()
	klaxID := d.store.SessionsFor("tg:1")[0].KlaxID

	md := "see [report](report.csv)"
	first, _ := d.rewriteOutboundForUI("tg:1", klaxID, 1, md)
	if !strings.Contains(first, "[report](/api/file?ref=") {
		t.Fatalf("first render must publish the file: %q", first)
	}

	if err := os.Rename(src, filepath.Join(t.TempDir(), "moved.csv")); err != nil {
		t.Fatal(err)
	}

	second, _ := d.rewriteOutboundForUI("tg:1", klaxID, 1, md)
	if second != first {
		t.Fatalf("a rebuild after the original vanished changed the link:\n first=%q\nsecond=%q", first, second)
	}
	if !strings.Contains(second, "/api/file?ref=") {
		t.Fatalf("link degraded to plain text although the snapshot is in the store: %q", second)
	}
}

// A later turn linking the same path serves that turn's content; the earlier turn keeps its own.
func TestRewriteOutboundCapturesEachTurnsVersion(t *testing.T) {
	t.Setenv("KLAX_DATA_DIR", t.TempDir())
	cwd := t.TempDir()
	src := filepath.Join(cwd, "summary.md")
	if err := os.WriteFile(src, []byte("# v1\n"), 0600); err != nil {
		t.Fatal(err)
	}

	d := newTestDeliveryDaemon(&fakeTransport{})
	d.store = &session.Store{Chats: map[string]*session.ChatSessions{}, Scope: map[string]*session.ScopeDefaults{}}
	d.store.New("tg:1", "one", cwd, session.ScopeDefaults{})
	d.runners = make(map[runnerKey]*sessionRunner)
	d.uiHub = newUIHub()
	klaxID := d.store.SessionsFor("tg:1")[0].KlaxID

	md := "готово [summary](summary.md)"
	turn1, _ := d.rewriteOutboundForUI("tg:1", klaxID, 1, md)

	if err := os.WriteFile(src, []byte("# v2 corrected\n"), 0600); err != nil {
		t.Fatal(err)
	}
	turn2, _ := d.rewriteOutboundForUI("tg:1", klaxID, 2, md)

	if turn2 == turn1 {
		t.Fatalf("the corrected file was served as the old snapshot — the fix is invisible: %q", turn2)
	}
	if again, _ := d.rewriteOutboundForUI("tg:1", klaxID, 1, md); again != turn1 {
		t.Fatalf("re-rendering turn 1 changed its link:\n was=%q\nnow=%q", turn1, again)
	}
	body1, body2 := servedBody(t, d, turn1), servedBody(t, d, turn2)
	if body1 != "# v1\n" {
		t.Fatalf("turn 1 serves %q, want the version it delivered", body1)
	}
	if body2 != "# v2 corrected\n" {
		t.Fatalf("turn 2 serves %q, want the corrected version", body2)
	}
}

// servedBody resolves a rewritten link back to the bytes it serves.
func servedBody(t *testing.T, d *daemon, rewritten string) string {
	t.Helper()
	i := strings.Index(rewritten, "ref=")
	if i < 0 {
		t.Fatalf("no capability ref in %q", rewritten)
	}
	tok := rewritten[i+len("ref="):]
	if j := strings.IndexAny(tok, "&)"); j >= 0 {
		tok = tok[:j]
	}
	d.fileTokensMu.Lock()
	ref, ok := d.fileTokens[tok]
	d.fileTokensMu.Unlock()
	if !ok {
		t.Fatalf("token %q not in the index", tok)
	}
	data, err := os.ReadFile(sessfiles.Open(ref.sk, ref.klaxID).Path(ref.stored))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A fresh Store (a restart) resolves the same link.
func TestRewriteOutboundResolvesAfterRestart(t *testing.T) {
	t.Setenv("KLAX_DATA_DIR", t.TempDir())
	cwd := t.TempDir()
	src := filepath.Join(cwd, "report.csv")
	if err := os.WriteFile(src, []byte("a,b\n1,2\n"), 0600); err != nil {
		t.Fatal(err)
	}

	newDaemon := func(klaxID string) *daemon {
		d := newTestDeliveryDaemon(&fakeTransport{})
		d.store = &session.Store{Chats: map[string]*session.ChatSessions{}, Scope: map[string]*session.ScopeDefaults{}}
		d.store.Add("tg:1", &session.Session{Name: "one", CWD: cwd})
		d.store.UpdateActive("tg:1", func(s *session.Session) { s.KlaxID = klaxID })
		d.runners = make(map[runnerKey]*sessionRunner)
		d.uiHub = newUIHub()
		return d
	}

	md := "see [report](report.csv)"
	first, _ := newDaemon("lOGezVsS").rewriteOutboundForUI("tg:1", "lOGezVsS", 1, md)
	if !strings.Contains(first, "/api/file?ref=") {
		t.Fatalf("first render must publish the file: %q", first)
	}
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}

	second, _ := newDaemon("lOGezVsS").rewriteOutboundForUI("tg:1", "lOGezVsS", 1, md)
	if second != first {
		t.Fatalf("after restart the link changed:\n first=%q\nsecond=%q", first, second)
	}
}

func TestOutboundFilesPolicy(t *testing.T) {
	cwd := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(cwd, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("ok.pdf", []byte("pdf"))
	write(".env", []byte("TOKEN=secret"))
	write("private.pem", []byte("key"))
	write(".npmrc", []byte("//registry:_authToken=x"))
	write("infra.tfstate", []byte("{}"))
	write("empty.txt", nil)
	write("large.bin", make([]byte, maxOutboundFileSize+1))

	md := "[ok](ok.pdf) [.env](.env) [key](private.pem) [npm](.npmrc) [tf](infra.tfstate) " +
		"[empty](empty.txt) [large](large.bin)"
	got := outboundFileRefs(md, cwd)
	if len(got) != 1 || got[0].name != "ok.pdf" {
		t.Fatalf("policy result = %#v, want only ok.pdf", got)
	}
	ct, data, err := got[0].load()
	if err != nil || string(data) != "pdf" {
		t.Fatalf("load() = %q, %q, %v; want the file bytes", ct, data, err)
	}
	if !strings.HasPrefix(ct, "application/pdf") {
		t.Fatalf("content type = %q, want application/pdf", ct)
	}
}

// Collection must not read file bytes: an answer linking many large files would
// otherwise hold all of them in memory at once, before the first one is sent.
func TestOutboundFileRefsDeferReads(t *testing.T) {
	cwd := t.TempDir()
	var md strings.Builder
	for i := 0; i < maxOutboundFiles+4; i++ {
		name := fmt.Sprintf("f%d.bin", i)
		if err := os.WriteFile(filepath.Join(cwd, name), []byte("payload"), 0600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&md, "[f](%s) ", name)
	}
	refs := outboundFileRefs(md.String(), cwd)
	if len(refs) != maxOutboundFiles {
		t.Fatalf("collected %d refs, want the %d-file budget", len(refs), maxOutboundFiles)
	}
	// Deleting the originals after collection must break load(): if the bytes
	// had been captured up front, this would still succeed.
	for _, ref := range refs {
		if err := os.Remove(ref.path); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := refs[0].load(); err == nil {
		t.Fatalf("load() succeeded for a deleted file — the payload was read during collection")
	}
}

func TestOutboundFilesRejectsOutsideRootAndRemoteLinks(t *testing.T) {
	cwd, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	md := "[outside](" + filepath.Join(outside, "secret.txt") + ") [web](https://example.test/a) [anchor](#x)"
	if got := outboundFileRefs(md, cwd); len(got) != 0 {
		t.Fatalf("outboundFileRefs returned files for unsafe links: %#v", got)
	}
}

// Codex answers link their own output as "sandbox:/abs/path" — a convention,
// not a real URI scheme. Reproduces the exact link Irina's session emitted:
// non-ASCII filename, spaces, url-escaped. Claude never uses this prefix, so
// this path is codex-only.
func TestOutboundFilesResolvesCodexSandboxLinks(t *testing.T) {
	cwd := t.TempDir()
	name := "УМК к лекции 6. Ароматные воды и сиропы.docx"
	if err := os.WriteFile(filepath.Join(cwd, name), []byte("docx"), 0600); err != nil {
		t.Fatal(err)
	}
	// codex escapes only the filename segment, keeping path separators intact.
	href := "sandbox:" + cwd + "/" + url.PathEscape(name)
	md := "[Скачать файл Word](" + href + ")"

	got := outboundFileRefs(md, cwd)
	if len(got) != 1 {
		t.Fatalf("outboundFileRefs did not resolve the sandbox: link: %#v (href=%q)", got, href)
	}
	if got[0].name != name {
		t.Fatalf("resolved name = %q, want %q", got[0].name, name)
	}
}

// Codex emits :codex-file-citation{path="..." purpose="output"} instead of a
// markdown link. Reproduces Irina's answer: raw non-ASCII path, no escaping.
func TestOutboundFilesResolvesCodexFileCitation(t *testing.T) {
	cwd := t.TempDir()
	name := "УМК_ПМ02 исправленная.docx"
	if err := os.WriteFile(filepath.Join(cwd, name), []byte("docx"), 0600); err != nil {
		t.Fatal(err)
	}
	md := "Доработанная УМК в формате Word:\n\n:codex-file-citation{path=\"" + filepath.Join(cwd, name) + "\" purpose=\"output\"}"

	got := outboundFileRefs(md, cwd)
	if len(got) != 1 || got[0].name != name {
		t.Fatalf("outboundFileRefs did not resolve codex-file-citation: %#v", got)
	}
	if s := stripFileCitations(md); s != "Доработанная УМК в формате Word:" {
		t.Fatalf("stripFileCitations = %q", s)
	}
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "x.txt"), []byte("x"), 0600)
	if got := outboundFileRefs(`:codex-file-citation{path="`+filepath.Join(outside, "x.txt")+`"}`, cwd); len(got) != 0 {
		t.Fatalf("citation outside cwd must not be delivered: %#v", got)
	}
}

// outbound_files=false keeps messenger answers text-only.
func TestOutboundFilesDisabledByConfig(t *testing.T) {
	off := false
	d := &daemon{cfg: &config.Config{OutboundFiles: &off}}
	if d.outboundFilesEnabled() {
		t.Fatalf("outbound_files=false must disable chat uploads")
	}
	d = &daemon{cfg: &config.Config{}}
	if !d.outboundFilesEnabled() {
		t.Fatalf("an unset outbound_files must stay enabled")
	}
}

// fakeFileTransport is a transport that can also upload files.
type fakeFileTransport struct {
	fakeTransport
	attempts  int
	sent      []string
	sendErrFn func(attempt int) error
}

func (f *fakeFileTransport) SendFile(chatID, name, contentType string, data []byte, caption, replyTo string) error {
	f.attempts++
	if f.sendErrFn != nil {
		if err := f.sendErrFn(f.attempts); err != nil {
			return err
		}
	}
	f.sent = append(f.sent, name+":"+string(data))
	return nil
}

// newFileDeliveryFixture wires a messengerDelivery over a session whose CWD holds one file.
func newFileDeliveryFixture(t *testing.T, ctx context.Context, tp *fakeFileTransport) *messengerDelivery {
	t.Helper()
	t.Setenv("KLAX_DATA_DIR", t.TempDir())
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "report.csv"), []byte("a,b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	d := newTestDeliveryDaemon(tp)
	d.cfg = &config.Config{}
	d.store = &session.Store{Chats: map[string]*session.ChatSessions{}, Scope: map[string]*session.ScopeDefaults{}}
	d.store.New("tg:1", "one", cwd, session.ScopeDefaults{})
	klaxID := d.store.SessionsFor("tg:1")[0].KlaxID
	return &messengerDelivery{d: d, ctx: ctx, chatID: "tg:1", sessionKey: "tg:1", sessionKlaxID: klaxID}
}

func TestSendOutboundFilesDelivers(t *testing.T) {
	tp := &fakeFileTransport{}
	m := newFileDeliveryFixture(t, context.Background(), tp)

	m.sendOutboundFiles("готово [csv](report.csv)")

	if len(tp.sent) != 1 || tp.sent[0] != "report.csv:a,b\n" {
		t.Fatalf("sent = %v, want the linked file", tp.sent)
	}
	if tp.sendCalls != 0 {
		t.Fatalf("a successful upload must not post a warning message (%d sends)", tp.sendCalls)
	}
}

// A permanent API error is not retried, and the user is told the file did not
// make it — otherwise the answer links to an attachment that never arrives.
func TestSendOutboundFilesReportsPermanentFailure(t *testing.T) {
	tp := &fakeFileTransport{sendErrFn: func(int) error {
		return &transport.APIError{Platform: "tg", Code: 403, Description: "forbidden"}
	}}
	m := newFileDeliveryFixture(t, context.Background(), tp)

	m.sendOutboundFiles("готово [csv](report.csv)")

	if tp.attempts != 1 {
		t.Fatalf("upload attempts = %d, want 1 (403 is permanent)", tp.attempts)
	}
	if tp.sendCalls != 1 || !strings.Contains(tp.sendLog[0].text, "report.csv") {
		t.Fatalf("expected a warning naming the file, got %v", tp.sendLog)
	}
}

// An aborted turn goes through retryDo's context check: nothing is attempted and
// the user gets no spurious warning for their own /abort.
func TestSendOutboundFilesRespectsAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tp := &fakeFileTransport{}
	m := newFileDeliveryFixture(t, ctx, tp)

	m.sendOutboundFiles("готово [csv](report.csv)")

	if tp.attempts != 0 {
		t.Fatalf("upload attempts = %d, want none after /abort", tp.attempts)
	}
	if tp.sendCalls != 0 {
		t.Fatalf("an aborted turn must not warn about undelivered files (%d sends)", tp.sendCalls)
	}
}

func TestSendOutboundFilesHonorsConfigToggle(t *testing.T) {
	tp := &fakeFileTransport{}
	m := newFileDeliveryFixture(t, context.Background(), tp)
	off := false
	m.d.cfg.OutboundFiles = &off

	m.sendOutboundFiles("готово [csv](report.csv)")

	if tp.attempts != 0 {
		t.Fatalf("outbound_files=false must not upload anything (%d attempts)", tp.attempts)
	}
}
