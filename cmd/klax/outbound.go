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

// outCitationRe matches the file directive codex emits instead of a markdown
// link: :codex-file-citation{path="/abs/path" purpose="output"}. The path is
// raw (not url-escaped) and may contain spaces or non-ASCII.
var outCitationRe = regexp.MustCompile(`:codex-file-citation\{[^}]*?path="([^"]+)"[^}]*\}`)

// stripFileCitations removes codex file directives from chat text once the
// files they name are delivered as attachments.
func stripFileCitations(md string) string {
	if !strings.Contains(md, ":codex-file-citation{") {
		return md
	}
	return strings.TrimSpace(outCitationRe.ReplaceAllString(md, ""))
}

// maxOutboundFiles caps how many local files one answer can publish (a budget, not a
// security boundary — confinement does that).
const maxOutboundFiles = 16

// Telegram's sendDocument limit is 50 MB. Keep one conservative limit for
// every messenger so the same answer behaves consistently in Telegram and
// MAX (whose limits are larger for some media types).
const maxOutboundFileSize = 50 << 20

var blockedOutboundExtensions = map[string]bool{
	".key": true, ".pem": true, ".p12": true, ".pfx": true,
	".jks": true, ".keystore": true, ".p8": true, ".kdbx": true,
	".tfstate": true,
}

// blockedOutboundNames are files whose whole purpose is to hold credentials.
// Unlike the UI, a chat upload cannot be revoked, so these never leave the host
// even when the agent links them deliberately.
var blockedOutboundNames = map[string]bool{
	"id_rsa": true, "id_ecdsa": true, "id_ed25519": true,
	"authorized_keys": true, "known_hosts": true,
	".netrc": true, ".npmrc": true, ".pgpass": true, "credentials": true,
}

// blockedOutboundDirs are path components that disqualify everything below them.
var blockedOutboundDirs = map[string]bool{
	".git": true, ".ssh": true, ".gnupg": true, ".aws": true, ".klax": true,
}

// outboundFileRef is one publishable local file. Only the path travels through
// the collection pass — the bytes are read one file at a time at send time, so
// a burst of large attachments cannot hold maxOutboundFiles × maxOutboundFileSize
// in memory at once.
type outboundFileRef struct {
	path string // symlink-resolved absolute path
	name string // filename as it appears in the chat
}

// outboundFilesEnabled reports whether this daemon may upload answer-linked
// files to messengers. An absent config means the default: enabled.
func (d *daemon) outboundFilesEnabled() bool {
	return d.cfg == nil || d.cfg.FileDeliveryEnabled()
}

// outboundFileRefs returns local files referenced by an agent answer. The same
// confinement rule as the web UI is used: only existing files below the
// session working directory are eligible for upload.
func outboundFileRefs(md, cwd string) []outboundFileRef {
	if md == "" || cwd == "" || (!strings.Contains(md, "](") && !strings.Contains(md, ":codex-file-citation{")) {
		return nil
	}
	var hrefs []string
	for _, m := range outLinkRe.FindAllStringSubmatch(md, -1) {
		hrefs = append(hrefs, m[3])
	}
	for _, m := range outCitationRe.FindAllStringSubmatch(md, -1) {
		// The directive's path is raw; escape it so outboundPath's unescape is a no-op.
		hrefs = append(hrefs, (&url.URL{Path: m[1]}).EscapedPath())
	}
	seen := make(map[string]bool)
	var out []outboundFileRef
	for _, href := range hrefs {
		if len(out) >= maxOutboundFiles {
			break
		}
		if isRemoteHref(href) {
			continue
		}
		real, ok := resolveInRoot(href, cwd, []string{cwd})
		if !ok || seen[real] || !outboundFileAllowed(real) {
			continue
		}
		// Cheap pre-filter so an oversized or empty file is skipped before it is
		// opened; readOutboundFile re-checks, which is what actually holds.
		if st, err := os.Stat(real); err != nil || !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > maxOutboundFileSize {
			continue
		}
		seen[real] = true
		out = append(out, outboundFileRef{path: real, name: sanitizeAttachmentFilename(filepath.Base(real))})
	}
	return out
}

// load reads one file's bytes and decides its content type.
func (r outboundFileRef) load() (contentType string, data []byte, err error) {
	data, err = readOutboundFile(r.path)
	if err != nil {
		return "", nil, err
	}
	ct := mime.TypeByExtension(filepath.Ext(r.path))
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	return ct, data, nil
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
	clean := filepath.Clean(path)
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if blockedOutboundDirs[strings.ToLower(part)] {
			return false
		}
	}
	base := strings.ToLower(filepath.Base(clean))
	if base == ".env" || strings.HasPrefix(base, ".env.") || blockedOutboundNames[base] {
		return false
	}
	return !blockedOutboundExtensions[strings.ToLower(filepath.Ext(base))]
}

// rewriteOutboundForUI rewrites an agent answer's local file links to /api/file?ref= capability
// URLs. A link that cannot be confined or snapshotted degrades to its plain label; published
// reports whether none did, so a caller may keep the result. UI-only.
func (d *daemon) rewriteOutboundForUI(sk, klaxID string, turnSeq int64, md string) (out string, published bool) {
	if d.uiHub == nil || md == "" || !strings.Contains(md, "](") {
		return md, true
	}
	// Store first, then liveness: closeSession deletes the session before dropping the runner, so a
	// missing session here means a concurrent close won.
	store := d.sessionStore(sk, klaxID)
	sess := d.store.Get(sk, klaxID)
	if sess == nil {
		return md, false
	}
	roots := []string{sess.CWD}
	n := 0
	published = true
	degrade := func(label string) string {
		published = false
		return label
	}
	out = outLinkRe.ReplaceAllStringFunc(md, func(m string) string {
		sub := outLinkRe.FindStringSubmatch(m)
		bang, label, href := sub[1], sub[2], sub[3]
		if isRemoteHref(href) {
			return m // http(s)/data/anchor/already-ours: leave untouched
		}
		if n >= maxOutboundFiles {
			return degrade(label)
		}
		key, keyOK := outboundKey(turnSeq, href, sess.CWD)
		if keyOK {
			if stored, ok := store.SourceStored(key); ok {
				if out, ok := d.storedHref(store, sk, klaxID, stored, bang, label); ok {
					n++
					return out
				}
				return degrade(label)
			}
		}
		// Not snapshotted yet: the one point the original is read, and where confinement applies.
		real, ok := resolveInRoot(href, sess.CWD, roots)
		if !ok {
			return degrade(label) // outside any root / malformed: degrade to text, never a dead link
		}
		stored, fi, err := store.Adopt(filepath.Base(real), real)
		if err != nil {
			return degrade(label)
		}
		// Token, turn mapping and content identity are one durable write, not three.
		token, err := d.commitLink(store, sk, klaxID, sessfiles.LinkRecord{
			Blob: stored, Name: sessfiles.DisplayName(stored),
			ContentType: mime.TypeByExtension(filepath.Ext(stored)),
			Source:      key, SeenPath: real, SeenInfo: fi,
		})
		if err != nil {
			return degrade(label) // a link that cannot be re-resolved later is not published
		}
		n++
		return renderHref(store, stored, token, bang, label)
	})
	return out, published
}

// storedHref renders an already-published blob as its markdown link.
func (d *daemon) storedHref(store *sessfiles.Store, sk string, klaxID string, stored, bang, label string) (string, bool) {
	token, err := d.fileToken(store, sk, klaxID, stored, sessfiles.DisplayName(stored), mime.TypeByExtension(filepath.Ext(stored)))
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
	// Codex links files inside its own sandbox as "sandbox:/abs/path" — its own
	// convention, not a real URI scheme. Left unstripped, "sandbox:/..." fails
	// filepath.IsAbs (it doesn't start with "/"), gets joined onto cwd into a
	// path that never exists, and the link is silently dropped: Claude never
	// emits this prefix, so only codex-backed sessions hit it. Compare only the
	// ASCII prefix itself (not a lowered copy of the whole string) so a non-ASCII
	// filename after it can't shift the byte offset used to strip the prefix.
	const sandboxPrefix = "sandbox:"
	if len(dec) >= len(sandboxPrefix) && strings.EqualFold(dec[:len(sandboxPrefix)], sandboxPrefix) {
		dec = dec[len(sandboxPrefix):]
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
