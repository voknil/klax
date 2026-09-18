package main

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/PiDmitrius/klax/internal/sessfiles"
)

// outLinkRe matches a markdown link or image: optional '!', [label](href) with a
// whitespace-free href.
var outLinkRe = regexp.MustCompile(`(!?)\[([^\]]*)\]\(([^)\s]+)\)`)

// maxOutboundFiles caps how many local files one answer can publish (a budget, not a
// security boundary — confinement does that).
const maxOutboundFiles = 16

// Telegram's sendDocument limit is 50 MB. Keep one conservative limit for
// every messenger so the same answer behaves consistently in Telegram and
// MAX (whose limits are larger for some media types).
const maxOutboundFileSize = 50 << 20

var blockedOutboundExtensions = map[string]bool{
	".key": true, ".pem": true, ".p12": true, ".pfx": true,
	".jks": true, ".keystore": true,
}

// outboundFiles returns local files referenced by an agent answer. The same
// confinement rule as the web UI is used: only existing files below the
// session working directory are eligible for upload.
func outboundFiles(md, cwd string) []struct {
	name, contentType string
	data              []byte
} {
	if md == "" || cwd == "" || !strings.Contains(md, "](") {
		return nil
	}
	seen := make(map[string]bool)
	var out []struct {
		name, contentType string
		data              []byte
	}
	outLinkRe.ReplaceAllStringFunc(md, func(m string) string {
		if len(out) >= maxOutboundFiles {
			return m
		}
		sub := outLinkRe.FindStringSubmatch(m)
		href := sub[3]
		if isRemoteHref(href) {
			return m
		}
		real, ok := resolveInRoot(href, cwd, []string{cwd})
		if !ok || seen[real] {
			return m
		}
		if !outboundFileAllowed(real) {
			return m
		}
		data, err := readOutboundFile(real)
		if err != nil {
			return m
		}
		seen[real] = true
		ct := mime.TypeByExtension(filepath.Ext(real))
		if ct == "" {
			ct = http.DetectContentType(data)
		}
		out = append(out, struct {
			name, contentType string
			data              []byte
		}{sanitizeAttachmentFilename(filepath.Base(real)), ct, data})
		return m
	})
	return out
}

// readOutboundFile re-checks the size while reading. A stat-then-ReadFile
// sequence alone is racy if the agent replaces the file between those calls.
// The extra byte lets us reject growth past the transport limit without
// allocating unbounded memory.
func readOutboundFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxOutboundFileSize {
		if err == nil {
			err = fmt.Errorf("file is empty, non-regular, or exceeds %d bytes", maxOutboundFileSize)
		}
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxOutboundFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxOutboundFileSize {
		return nil, fmt.Errorf("file exceeds %d bytes", maxOutboundFileSize)
	}
	return data, nil
}

// outboundFileAllowed blocks common credential/key locations. The file-link
// UI has its own capability controls; messenger uploads need this additional
// guard because the bytes leave the host and cannot be revoked afterwards.
func outboundFileAllowed(path string) bool {
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		lower := strings.ToLower(part)
		if lower == ".git" || lower == ".ssh" {
			return false
		}
	}
	base := strings.ToLower(filepath.Base(path))
	if base == ".env" || strings.HasPrefix(base, ".env.") || base == "id_rsa" || base == "id_ed25519" || base == "authorized_keys" || base == "known_hosts" {
		return false
	}
	return !blockedOutboundExtensions[strings.ToLower(filepath.Ext(base))]
}

// rewriteOutboundForUI rewrites an agent answer's local file links to /api/file?ref= capability
// URLs. A link that cannot be confined or snapshotted degrades to its plain label. UI-only.
func (d *daemon) rewriteOutboundForUI(sk string, created, turnSeq int64, md string) string {
	if d.uiHub == nil || md == "" || !strings.Contains(md, "](") {
		return md
	}
	// Store first, then liveness: closeSession deletes the session before dropping the runner, so a
	// missing session here means a concurrent close won.
	store := d.sessionStore(sk, created)
	sess := d.store.Get(sk, created)
	if sess == nil {
		return md
	}
	roots := []string{sess.CWD}
	n := 0
	return outLinkRe.ReplaceAllStringFunc(md, func(m string) string {
		sub := outLinkRe.FindStringSubmatch(m)
		bang, label, href := sub[1], sub[2], sub[3]
		if isRemoteHref(href) {
			return m // http(s)/data/anchor/already-ours: leave untouched
		}
		if n >= maxOutboundFiles {
			return label
		}
		key, keyOK := outboundKey(turnSeq, href, sess.CWD)
		if keyOK {
			if stored, ok := store.SourceStored(key); ok {
				if out, ok := d.storedHref(store, sk, created, stored, bang, label); ok {
					n++
					return out
				}
				return label
			}
		}
		// Not snapshotted yet: the one point the original is read, and where confinement applies.
		real, ok := resolveInRoot(href, sess.CWD, roots)
		if !ok {
			return label // outside any root / malformed: degrade to text, never a dead link
		}
		stored, fi, err := store.Adopt(filepath.Base(real), real)
		if err != nil {
			return label
		}
		// Token, turn mapping and content identity are one durable write, not three.
		token, err := d.commitLink(store, sk, created, sessfiles.LinkRecord{
			Blob: stored, Name: sessfiles.DisplayName(stored),
			ContentType: mime.TypeByExtension(filepath.Ext(stored)),
			Source:      key, SeenPath: real, SeenInfo: fi,
		})
		if err != nil {
			return label // a link that cannot be re-resolved later is not published
		}
		n++
		return renderHref(store, stored, token, bang, label)
	})
}

// storedHref renders an already-published blob as its markdown link.
func (d *daemon) storedHref(store *sessfiles.Store, sk string, created int64, stored, bang, label string) (string, bool) {
	token, err := d.fileToken(store, sk, created, stored, sessfiles.DisplayName(stored), mime.TypeByExtension(filepath.Ext(stored)))
	if err != nil {
		return "", false
	}
	return renderHref(store, stored, token, bang, label), true
}

// renderHref builds the capability URL, with image dimensions read from the blob.
func renderHref(store *sessfiles.Store, stored, token, bang, label string) string {
	href := "/api/file?ref=" + url.QueryEscape(token)
	if bang == "!" {
		if w, h := imageDimensions(store.Path(stored)); w > 0 && h > 0 {
			href += fmt.Sprintf("&w=%d&h=%d", w, h)
		}
	}
	return bang + "[" + label + "](" + href + ")"
}

// isRemoteHref reports hrefs we must not treat as local files: remote schemes, data
// URIs, protocol-relative, anchors, and our own already-minted /api/ URLs.
func isRemoteHref(href string) bool {
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "/api/") {
		return true
	}
	l := strings.ToLower(href)
	for _, p := range []string{"http://", "https://", "data:", "mailto:", "//"} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

// outboundKey identifies one turn's reference to one file: "<turnSeq>:<absolute path>".
func outboundKey(turnSeq int64, href, cwd string) (string, bool) {
	p, ok := outboundPath(href, cwd)
	if !ok {
		return "", false
	}
	return strconv.FormatInt(turnSeq, 10) + ":" + p, true
}

// outboundPath resolves an href to the absolute path it names, by path arithmetic alone — no
// filesystem access, so it still answers for a file that has since been deleted. It is the single
// definition of which path an href means, shared by the durable key and by resolveInRoot.
func outboundPath(href, cwd string) (string, bool) {
	dec, err := url.PathUnescape(href)
	if err != nil {
		dec = href
	}
	if strings.HasPrefix(dec, "~") || strings.HasPrefix(strings.ToLower(dec), "file:") {
		return "", false
	}
	if strings.IndexFunc(dec, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", false
	}
	if i := strings.IndexByte(dec, '#'); i >= 0 {
		dec = dec[:i]
	}
	if dec == "" {
		return "", false
	}
	if filepath.IsAbs(dec) {
		return filepath.Clean(dec), true
	}
	if cwd == "" {
		return "", false
	}
	return filepath.Join(cwd, dec), true
}

// resolveInRoot resolves href to a symlink-resolved path inside a root. Requires the file to exist,
// so it is used only when adopting.
func resolveInRoot(href, cwd string, roots []string) (string, bool) {
	p, ok := outboundPath(href, cwd)
	if !ok {
		return "", false
	}
	if !pathInRoots(p, roots...) {
		return "", false
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", false
	}
	return real, true
}
