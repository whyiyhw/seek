package controlplane

// Attachment intake for the web page (W5, card c_56yrcj;
// feature-web-frontend §13.1).
//
// The page has three entry points — click-select, drag-and-drop, paste —
// and they all end in one POST /attach of raw bytes. What the server
// does is deliberately small: authenticate (the SAME stack as
// POST /msg — an attachment ends up driving a seek run), sniff the
// container, enforce the DOWNSTREAM lane's own cap, and stage the bytes
// under the serve dir.
//
// The plane gains NO attachment pipeline (frontend.go's contract note:
// "the plane stores each attachment to disk verbatim and injects an
// @<path> reference into Text"). The page appends "@<abs path>" tokens
// to the outgoing /msg text, and the child process's existing
// submit-time routers do the rest:
//
//   - images → cmd/seek's visionRouter: imgrefs.Detect (stat gate) →
//     modelcap.Vision(model) — the second-tier lanes' capability
//     decision — → assets.Store → ImagePart (124b6f4);
//   - documents → fileroute.Router: copy-at-submit + Files API upload,
//     or the lane note when the active lane has no file input.
//
// So the capability judgement, the asset naming, and the in-band
// failure notes stay in exactly one place, and the web channel inherits
// whatever lane the serve/CLI session runs on. The one thing this file
// must get right is that the path it returns is a valid @-reference:
// imgrefs/filerefs tokenize on whitespace, so the staged name is
// sanitised to [A-Za-z0-9._-] and a data dir containing whitespace is
// refused rather than silently handing back an unusable token.
//
//	POST /attach?name=<display name>   raw body → 201 {ok,path,name,kind,bytes}
//
// Caps mirror the lanes that consume the file (not a second opinion):
// images at assets.MaxAssetBytes (32 MiB), documents at
// deepseek.FileMaxBytes (100 MiB). The extension table mirrors
// assets.allowedExt ∪ filerefs' doc set — a type neither lane can take
// is refused HERE with a 415 instead of becoming an in-band note deep
// inside a run.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/assets"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

const (
	// attachDirName is the staging directory under the serve dir
	// (…/serve/attachments/<conv>/). Bytes live only until the child
	// run has copied them into the project's asset store, so the
	// per-conv sweep below is a bound on a staging area, not an archive.
	attachDirName = "attachments"
	// attachKeepPerConv bounds one conversation's staging directory.
	// A prune victim is at worst a file the user attached but never
	// sent; 32 covers a very chatty paste session.
	attachKeepPerConv = 32
	// attachStemMax bounds the sanitised stem so long names do not
	// dominate the path a transcript has to carry.
	attachStemMax = 40
)

// attachImageMIME is the image set assets.Store accepts — byte-for-byte
// the lane's own table. Files outside it would fail at Store time with
// an in-band note, so they are refused at the door instead.
var attachImageMIME = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
}

// attachDocExt is filerefs' document set (the Files API's parseable
// formats).
var attachDocExt = map[string]bool{
	".pdf": true, ".doc": true, ".docx": true, ".ppt": true, ".pptx": true,
}

// attachResponse is the 201 body. path is the @-reference token; name
// is the user's own filename (display only — it never enters the path);
// kind says which downstream router will claim it.
type attachResponse struct {
	OK    bool   `json:"ok"`
	Path  string `json:"path"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Bytes int64  `json:"bytes"`
}

// handleAttach stages one attachment. Auth first (write surface, §5.2:
// a cookie alone must not drive it — same writeGate as /msg and /voice),
// then method, then the extension table, which decides the cap BEFORE
// any bytes are read.
func (f *WebFrontend) handleAttach(w http.ResponseWriter, r *http.Request) {
	fail := func(code int, format string, args ...any) {
		writeJSON(w, code, map[string]string{"error": fmt.Sprintf(format, args...)})
	}
	switch f.authKind(r) {
	case credNone:
		fail(http.StatusUnauthorized, "invalid or missing token (use ?token= or Authorization: Bearer)")
		return
	case credCookie:
		if !f.writeGate(r) {
			fail(http.StatusForbidden, "cookie-authenticated writes need a same-origin Origin header or X-Seek-Token")
			return
		}
	}
	if r.Method != http.MethodPost {
		fail(http.StatusMethodNotAllowed, "POST only")
		return
	}
	conv, err := f.convFromRequest(r)
	if err != nil {
		fail(http.StatusBadRequest, "%v", err)
		return
	}

	display := strings.TrimSpace(r.URL.Query().Get("name"))
	if display == "" {
		display = "attachment"
	}
	base, ext := sanitizeAttachName(display)
	kind, capBytes := "file", int64(deepseek.FileMaxBytes)
	if attachImageMIME[ext] != "" {
		kind, capBytes = "image", int64(assets.MaxAssetBytes)
	} else if !attachDocExt[ext] {
		fail(http.StatusUnsupportedMediaType,
			"不支持的文件类型 %q（图片 png/jpg/jpeg/webp/gif；文档 pdf/doc/docx/ppt/pptx）", ext)
		return
	}

	root := filepath.Join(f.attachDir, conv.Native())
	if strings.ContainsAny(root, " \t\r\n") {
		// imgrefs/filerefs split the prompt on whitespace, so a staged
		// path with a space can never become a working @-reference.
		// Refuse loudly instead of accepting bytes the run would ignore.
		fail(http.StatusInternalServerError,
			"数据目录含空白字符（%q）：@路径 无法传给 CLI，请把 SEEK_HOME 指到无空白路径", root)
		return
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		fail(http.StatusInternalServerError, "创建附件目录失败：%v", err)
		return
	}

	// Cap + one byte so the boundary itself is detectable, mirroring
	// the /voice handler's shape.
	tmp, err := os.CreateTemp(root, "up-")
	if err != nil {
		fail(http.StatusInternalServerError, "创建临时文件失败：%v", err)
		return
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	n, err := io.Copy(tmp, io.LimitReader(r.Body, capBytes+1))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		fail(http.StatusBadRequest, "写入附件失败：%v", err)
		return
	}
	switch {
	case n == 0:
		cleanup()
		fail(http.StatusBadRequest, "文件为空")
		return
	case n > capBytes:
		cleanup()
		fail(http.StatusRequestEntityTooLarge, "%s 超过 %d MiB 上限", display, capBytes>>20)
		return
	}

	// Container check — a .png that is not a PNG (or a .pdf that is not
	// a PDF) would die inside the run as an in-band note, several
	// seconds later and with a worse sentence. Check the magic up
	// front so the failure is attributable to the file the user picked.
	head := make([]byte, 512)
	fh, err := os.Open(tmpName)
	if err == nil {
		var n2 int
		n2, err = fh.Read(head)
		fh.Close()
		if err == io.EOF {
			err = nil
		}
		head = head[:n2]
	}
	if err != nil {
		cleanup()
		fail(http.StatusInternalServerError, "读取附件失败：%v", err)
		return
	}
	if !attachMagicOK(ext, head) {
		cleanup()
		fail(http.StatusUnsupportedMediaType, "%s 的内容与 %s 扩展名不符（损坏或被改名）", display, ext)
		return
	}

	final := filepath.Join(root, fmt.Sprintf("%d-%s", time.Now().UnixNano(), base))
	if err := os.Rename(tmpName, final); err != nil {
		cleanup()
		fail(http.StatusInternalServerError, "保存附件失败：%v", err)
		return
	}
	pruneAttachments(root, attachKeepPerConv)

	writeJSON(w, http.StatusCreated, attachResponse{OK: true, Path: final, Name: display, Kind: kind, Bytes: n})
}

// attachMagicOK reports whether head matches the container ext claims.
// Images are checked with net/http's own sniffer, which covers exactly
// the five formats assets.Store accepts; documents check their three
// container magics directly (Go's sniffer has no CFB/Office row).
func attachMagicOK(ext string, head []byte) bool {
	if want := attachImageMIME[ext]; want != "" {
		got := http.DetectContentType(head)
		if i := strings.IndexByte(got, ';'); i >= 0 {
			got = got[:i]
		}
		return got == want
	}
	switch ext {
	case ".pdf":
		return bytes.HasPrefix(head, []byte("%PDF-"))
	case ".docx", ".pptx":
		// OOXML is a ZIP container; an empty archive is still a
		// (degenerate) document, so both the local-header and the
		// end-of-central-directory magics pass.
		return bytes.HasPrefix(head, []byte("PK\x03\x04")) || bytes.HasPrefix(head, []byte("PK\x05\x06"))
	default: // .doc / .ppt — OLE compound file
		return len(head) >= 8 && bytes.Equal(head[:8],
			[]byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1})
	}
}

// sanitizeAttachName turns a user filename into a token-safe base name
// plus its lowercased extension: path separators stripped (Windows
// backslashes included), everything outside [A-Za-z0-9._-] folded to
// '_', runs collapsed, stem bounded, and the extension preserved so the
// child's routers (both extension-keyed) still classify it.
func sanitizeAttachName(raw string) (string, string) {
	base := filepath.Base(strings.ReplaceAll(raw, `\`, "/"))
	if base == "." || base == ".." || base == "/" {
		base = ""
	}
	ext := strings.ToLower(filepath.Ext(base))
	stem := strings.TrimSuffix(base, filepath.Ext(base))

	var b strings.Builder
	lastUnderscore := false
	for _, r := range stem {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-'
		if ok {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	clean := strings.Trim(b.String(), "._-")
	if clean == "" {
		clean = "attachment"
	}
	if len(clean) > attachStemMax {
		// Byte-slicing can cut a multibyte rune — but every rune kept
		// above is ASCII, so this is safe by construction.
		clean = strings.Trim(clean[:attachStemMax], "._-")
		if clean == "" {
			clean = "attachment"
		}
	}
	return clean + ext, ext
}

// pruneAttachments keeps at most keep files in one conv's staging dir.
// Names carry a zero-padded-free UnixNano prefix, so a plain string sort
// is chronological ordering — no per-file stat, no reflect. Best-effort:
// a failed sweep costs disk, never the request.
func pruneAttachments(dir string, keep int) {
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) <= keep {
		return
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return
	}
	// Insertion sort: a staging dir holds dozens of names at most.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	for _, n := range names[:len(names)-keep] {
		_ = os.Remove(filepath.Join(dir, n))
	}
}
