// resolve.go — the send-time half of document input (card F4): the
// file sibling of ResolveImages / WithoutImages in vision.go.
//
// ResolveFiles owns the one thing submit-time routing cannot: a
// file_id's lifetime. F3 uploads eagerly, so a part normally arrives
// with a live id; but the API documents a 1 h – 30 day (or permanent)
// lifecycle, a resumed session can be older than its ids, and a submit
// whose upload failed already left the asset stored. Every such part
// is healed here by re-uploading the LOCAL asset — the server cannot
// re-download originals, so the asset library is the only source
// (feature-deepseek-app-api §3.1 持久层).
//
// Cost posture (why the cache below exists): an upload IS a parse, and
// parses bill ¥1.5/千页 — unlike re-referencing a live id, which is
// just input tokens. Since ResolveFiles returns a request copy and
// leaves the transcript untouched (the vision precedent: history keeps
// the durable form), history would otherwise re-upload an expired
// document on EVERY turn. The in-memory cache bounds that to one
// re-upload per asset per process. It is a transport cache, not state:
// dropping it costs at most one extra parse, which is why it needs no
// disk file and no reconstruct discipline (contrast AGENTS.md's
// parallel-state warning — nothing here can drift, because nothing
// reads it back).
package deepseek

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// FileLoader loads a persisted asset's raw bytes. The asset name doubles
// as the upload filename — content-addressed names keep their extension
// (internal/assets.StoreDoc appends it), and the server sniffs the
// format off that extension — so the loader returns bytes only.
type FileLoader func(asset string) (data []byte, err error)

// FileUploader is the one-method Files-API surface ResolveFiles needs
// for lazy / expiry re-uploads; *Client satisfies it, so tests fake it
// without HTTP.
type FileUploader interface {
	UploadFile(ctx context.Context, req *FileUploadRequest) (*FileObject, error)
}

// FileResolver materialises Asset references into live file ids at send
// time. One instance per session: the cache map is the process-scoped
// transport cache described in the file header. Its fields are exported
// so cmd/seek can build it without a constructor maze.
type FileResolver struct {
	Load   FileLoader   // nil → assets unavailable: expired/unuploaded parts degrade to notes
	Upload FileUploader // nil → no Files API on this lane: same degradation

	// Now is the clock expiry checks read. nil → time.Now. A seam so
	// tests can pin "this id expired" without sleeping.
	Now func() time.Time

	mu    sync.Mutex
	cache map[string]fileRef // fileCacheKey(asset,purpose) → newest known upload
}

// fileRef is one cached upload: the id, the parse purpose it was made
// with, and when it dies (0 = permanent).
type fileRef struct {
	id        string
	purpose   string
	expiresAt int64
}

// NewFileResolver builds a resolver around a loader and an uploader.
// Both may be nil (see the field docs); cmd/seek passes the project
// asset store's reader and its *deepseek.Client.
func NewFileResolver(load FileLoader, up FileUploader) *FileResolver {
	return &FileResolver{Load: load, Upload: up}
}

// ResolveFiles returns a request-ready copy of msgs in which every file
// part carries a live Files API id. Parts whose id is missing or past
// its recorded expiry are re-uploaded from the local asset; upload
// failures NEVER abort the send (never-error philosophy, inherited from
// ResolveImages): the part is dropped and an in-band note is appended
// to that message's Content so the model — and the transcript — see
// what the turn is missing. Messages without files pass through
// untouched; the input slice and its messages are never mutated.
//
// Call it on the send-time copy only. History keeps the durable form;
// the live id returned here is for this request (and the resolver's
// cache covers the next one).
func ResolveFiles(ctx context.Context, msgs []Message, r *FileResolver) []Message {
	has := false
	for _, m := range msgs {
		if len(m.Files) > 0 {
			has = true
			break
		}
	}
	if !has {
		return msgs
	}
	now := time.Now()
	if r != nil && r.Now != nil {
		now = r.Now()
	}
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		if len(m.Files) == 0 {
			out[i] = m
			continue
		}
		parts := make([]FilePart, 0, len(m.Files))
		for _, p := range m.Files {
			ref, ok := r.live(p, now) // cache hit or unchanged live id
			if !ok {
				var err error
				ref, err = r.upload(ctx, p)
				if err != nil {
					m.Content += fileResolveNote(p.Asset, err.Error())
					continue
				}
			}
			np := p
			np.FileID = ref.id
			np.ExpiresAt = ref.expiresAt
			np.Purpose = ref.purpose
			parts = append(parts, np)
		}
		if len(parts) == 0 {
			m.Files = nil
		} else {
			m.Files = parts
		}
		out[i] = m
	}
	return out
}

// live answers "can this part go on the wire as-is, or is a cached
// newer upload available?". Returned bool false means "must upload".
func (r *FileResolver) live(p FilePart, now time.Time) (fileRef, bool) {
	if r == nil {
		// Defensive: no resolver wired. A live id is still sendable —
		// the wire only needs file_id — but nothing can be healed.
		if p.FileID != "" && !expiredUnix(p.ExpiresAt, now) {
			return fileRef{id: p.FileID, purpose: p.Purpose, expiresAt: p.ExpiresAt}, true
		}
		return fileRef{}, false
	}
	if p.FileID != "" && !expiredUnix(p.ExpiresAt, now) {
		return fileRef{id: p.FileID, purpose: p.Purpose, expiresAt: p.ExpiresAt}, true
	}
	if p.Asset == "" {
		return fileRef{}, false
	}
	key := fileCacheKey(p.Asset, p.Purpose)
	r.mu.Lock()
	defer r.mu.Unlock()
	if ref, ok := r.cache[key]; ok && !expiredUnix(ref.expiresAt, now) {
		return ref, true
	}
	return fileRef{}, false
}

// upload re-uploads a part's asset and caches the result. Every failure
// mode here becomes an in-band note at the call site.
func (r *FileResolver) upload(ctx context.Context, p FilePart) (fileRef, error) {
	if r == nil {
		return fileRef{}, errors.New("no file resolver wired")
	}
	if p.Asset == "" {
		return fileRef{}, errors.New("file part has neither a live file_id nor an asset to re-upload")
	}
	if r.Upload == nil || r.Load == nil {
		return fileRef{}, errors.New("no Files API upload path on this lane")
	}
	data, err := r.Load(p.Asset)
	if err != nil {
		return fileRef{}, err
	}
	purpose := p.Purpose
	if purpose == "" {
		purpose = PurposeTextRecognition // legacy parts: text was F3's default
	}
	fo, err := r.Upload.UploadFile(ctx, &FileUploadRequest{
		Filename: p.Asset,
		Data:     data,
		Purpose:  purpose,
	})
	if err != nil {
		return fileRef{}, err
	}
	if fo == nil || fo.ID == "" {
		return fileRef{}, errors.New("upload returned no file id")
	}
	ref := fileRef{id: fo.ID, purpose: purpose, expiresAt: fo.ExpiresAt}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = make(map[string]fileRef)
	}
	r.cache[fileCacheKey(p.Asset, purpose)] = ref
	r.mu.Unlock()
	return ref, nil
}

// fileCacheKey scopes the transport cache by parse purpose: the same
// asset parsed once as text and once as rich yields DIFFERENT documents,
// so one id must not stand in for the other. Empty purpose normalises
// to the legacy default, the same rule upload applies on the wire.
func fileCacheKey(asset, purpose string) string {
	if purpose == "" {
		purpose = PurposeTextRecognition
	}
	return asset + "\x00" + purpose
}

// expiredUnix reports whether a recorded expiry has passed. 0/absent
// means "no expiry advertised" (permanent) — never expired.
func expiredUnix(expiresAt int64, now time.Time) bool {
	return expiresAt > 0 && now.Unix() >= expiresAt
}

// fileResolveNote is the document sibling of assetMissingNote: the same
// "[file: " family as the submit-time router's markers, so one replay
// styling rule covers every generation. Never fatal — the turn goes out
// without the document, with the reason in-band.
func fileResolveNote(asset, why string) string {
	name := asset
	if name == "" {
		name = "?"
	}
	return fmt.Sprintf("\n\n[file: %s — 附件不可用: %s]", name, why)
}

// WithoutFiles returns a copy of msgs with every Files field cleared.
// The in-band marker text inside Content is untouched, so the
// conversation stays self-describing. Used by side-channel one-shot
// calls (Summarise / compact) that shouldn't pay for — or depend on
// resolving — document ids; same rationale as WithoutImages.
func WithoutFiles(msgs []Message) []Message {
	has := false
	for _, m := range msgs {
		if len(m.Files) > 0 {
			has = true
			break
		}
	}
	if !has {
		return msgs
	}
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		m.Files = nil
		out[i] = m
	}
	return out
}
