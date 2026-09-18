package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PiDmitrius/klax/internal/config"
	"github.com/PiDmitrius/klax/internal/session"
)

func TestMigrateClaudeTranscriptMissingProjectsIsNoop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := migrateClaudeTranscript("/old", "/new", "session-id"); err != nil {
		t.Fatalf("migrateClaudeTranscript returned error for missing projects dir: %v", err)
	}
}

// The transcript and its sidecar directory follow the session to the new CWD's
// project dir — that is what makes /cwd keep the conversation.
func TestMigrateClaudeTranscriptCopiesTranscriptAndSidecar(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldCWD, newCWD := t.TempDir(), t.TempDir()

	base := filepath.Join(home, ".claude", "projects")
	srcDir := claudeProjectDir(base, oldCWD)
	if err := os.MkdirAll(filepath.Join(srcDir, "sess-1"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sess-1.jsonl"), []byte(`{"a":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sess-1", "note.txt"), []byte("side"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := migrateClaudeTranscript(oldCWD, newCWD, "sess-1"); err != nil {
		t.Fatalf("migrateClaudeTranscript: %v", err)
	}

	dstDir := claudeProjectDir(base, newCWD)
	if got, err := os.ReadFile(filepath.Join(dstDir, "sess-1.jsonl")); err != nil || string(got) != `{"a":1}` {
		t.Fatalf("transcript at new CWD = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dstDir, "sess-1", "note.txt")); err != nil || string(got) != "side" {
		t.Fatalf("sidecar at new CWD = %q, %v", got, err)
	}
	// The original stays put: a failed switch must not lose the history.
	if _, err := os.Stat(filepath.Join(srcDir, "sess-1.jsonl")); err != nil {
		t.Fatalf("original transcript disappeared: %v", err)
	}
}

// An existing session keeps its own CWD, so the inbound path must not copy
// transcripts into a directory no run will ever use.
func TestEnsureSessionWithCWDDoesNotMigrateExistingSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KLAX_DATA_DIR", t.TempDir())
	sessCWD, forced := t.TempDir(), t.TempDir()

	st, err := session.LoadStore()
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	d := &daemon{cfg: &config.Config{}, store: st}
	st.New("tg:1", "one", sessCWD, session.ScopeDefaults{})
	sess := st.Active("tg:1")

	base := filepath.Join(home, ".claude", "projects")
	srcDir := claudeProjectDir(base, sessCWD)
	if err := os.MkdirAll(srcDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, sess.ID+".jsonl"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	d.ensureSessionWithCWD("tg:1", forced)

	if got := st.Active("tg:1"); got.CWD != sessCWD {
		t.Fatalf("session cwd = %q, want the session's own %q", got.CWD, sessCWD)
	}
	if _, err := os.Stat(filepath.Join(claudeProjectDir(base, forced), sess.ID+".jsonl")); !os.IsNotExist(err) {
		t.Fatalf("transcript was copied to the forced CWD that no run reads (err=%v)", err)
	}
}
