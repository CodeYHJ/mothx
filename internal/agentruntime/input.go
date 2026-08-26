package agentruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/session"
)

// AttachmentKind identifies the two media classes supported by channel input.
// Additional provider artifacts use provider.Attachment and are normalized into
// this store only when they become concrete files.
type AttachmentKind string

const (
	AttachmentImage AttachmentKind = "image"
	AttachmentFile  AttachmentKind = "file"
)

// RunInput is the only user-input contract consumed by SessionRuntime. Frontend
// adapters may decode their wire format, but must not construct provider
// content or maintain a parallel string-only execution path.
type RunInput struct {
	Text        string
	Attachments []InputAttachment
}

// InputAttachment references an attachment already accepted by AttachmentService.
type InputAttachment struct {
	AttachmentID string
	Kind         AttachmentKind
	Filename     string
	MediaType    string
	Bytes        int64
}

// AttachmentIngress is an ephemeral transport-to-runtime handoff. Open must
// read from an authenticated platform source; its reference is never treated
// as an arbitrary URL or local path by the runtime.
type AttachmentIngress struct {
	Origin    string
	Reference string
	MessageID string
	Kind      AttachmentKind
	Filename  string
	MediaType string
	SizeHint  int64
	Open      func(context.Context) (AttachmentStream, error)
}

// AttachmentStream is the authenticated one-shot source supplied by a thin
// adapter. Runtime owns the copy, validation, closing, and durable record.
// Transport metadata may refine the event metadata when the platform only
// reveals a filename or media type on download.
type AttachmentStream struct {
	Reader      io.ReadCloser
	Filename    string
	MediaType   string
	ContentSize int64
}

// SessionAttachment is the canonical persisted attachment record. The content
// itself lives under StorageKey and is never embedded in a session entry.
type SessionAttachment struct {
	ID         string
	SessionID  string
	RunID      string
	Origin     string
	Kind       AttachmentKind
	Filename   string
	MediaType  string
	Bytes      int64
	SHA256     string
	StorageKey string
	Status     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

// AttachmentPolicy contains the local resource limits for accepted media.
// These are reliability limits for a self-hosted service, not a moderation or
// multi-tenant authorization policy.
type AttachmentPolicy struct {
	MaxImageBytes int64
	MaxFileBytes  int64
	Retention     time.Duration
}

func DefaultAttachmentPolicy() AttachmentPolicy {
	return AttachmentPolicy{
		MaxImageBytes: 20 << 20,
		MaxFileBytes:  50 << 20,
		Retention:     7 * 24 * time.Hour,
	}
}

// AttachmentService owns attachment storage and its session-backed records.
// Platform adapters provide only the authenticated Open function.
type AttachmentService struct {
	sessionDir string
	policy     AttachmentPolicy
}

func NewAttachmentService(sessionDir string, policy AttachmentPolicy) (*AttachmentService, error) {
	if strings.TrimSpace(sessionDir) == "" {
		return nil, fmt.Errorf("attachment session directory is required")
	}
	if policy.MaxImageBytes <= 0 || policy.MaxFileBytes <= 0 {
		return nil, fmt.Errorf("attachment size limits must be positive")
	}
	if policy.Retention <= 0 {
		return nil, fmt.Errorf("attachment retention must be positive")
	}
	return &AttachmentService{sessionDir: filepath.Clean(sessionDir), policy: policy}, nil
}

func (s *AttachmentService) Policy() AttachmentPolicy {
	if s == nil {
		return AttachmentPolicy{}
	}
	return s.policy
}

// Accept downloads and persists one platform attachment. It is deliberately
// stream based so a rejected oversized file is never held in memory.
func (s *AttachmentService) Accept(ctx context.Context, sessionID, runID string, ingress AttachmentIngress) (SessionAttachment, error) {
	if s == nil {
		return SessionAttachment{}, fmt.Errorf("attachment service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(sessionID) == "" {
		return SessionAttachment{}, fmt.Errorf("attachment session ID is required")
	}
	if ingress.Open == nil {
		return SessionAttachment{}, fmt.Errorf("attachment source is not readable")
	}
	if ingress.Kind != AttachmentImage && ingress.Kind != AttachmentFile {
		return SessionAttachment{}, fmt.Errorf("unsupported attachment kind %q", ingress.Kind)
	}
	// Best-effort expiry cleanup belongs to the single Runtime-owned store. A
	// cleanup hiccup must not make a trusted self-hosted user's new attachment
	// unusable; its durable write below remains the authoritative operation.
	_, _ = s.CleanupExpired(ctx)

	maxBytes := s.policy.MaxFileBytes
	if ingress.Kind == AttachmentImage {
		maxBytes = s.policy.MaxImageBytes
	}
	if ingress.SizeHint > maxBytes {
		return SessionAttachment{}, fmt.Errorf("attachment exceeds %d bytes", maxBytes)
	}

	attachmentID := session.GenerateID()
	if err := validatePathComponent(sessionID); err != nil {
		return SessionAttachment{}, err
	}
	if err := validatePathComponent(attachmentID); err != nil {
		return SessionAttachment{}, err
	}
	dir := filepath.Join(s.sessionDir, "attachments", sessionID, attachmentID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return SessionAttachment{}, fmt.Errorf("create attachment directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".incoming-*")
	if err != nil {
		return SessionAttachment{}, fmt.Errorf("create attachment temporary file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	stream, err := ingress.Open(ctx)
	if err != nil {
		cleanup()
		return SessionAttachment{}, fmt.Errorf("open %s attachment: %w", ingress.Origin, err)
	}
	reader := stream.Reader
	if reader == nil {
		cleanup()
		return SessionAttachment{}, fmt.Errorf("open %s attachment: empty reader", ingress.Origin)
	}
	defer reader.Close()

	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(reader, maxBytes+1))
	if err != nil {
		cleanup()
		return SessionAttachment{}, fmt.Errorf("read attachment: %w", err)
	}
	if written > maxBytes {
		cleanup()
		return SessionAttachment{}, fmt.Errorf("attachment exceeds %d bytes", maxBytes)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return SessionAttachment{}, fmt.Errorf("sync attachment: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return SessionAttachment{}, fmt.Errorf("close attachment: %w", err)
	}

	filename := ingress.Filename
	if strings.TrimSpace(filename) == "" {
		filename = stream.Filename
	}
	filename = sanitizeAttachmentFilename(filename)
	mediaType := strings.TrimSpace(ingress.MediaType)
	if mediaType == "" {
		mediaType = strings.TrimSpace(stream.MediaType)
	}
	detectedType, detectErr := detectAttachmentMediaType(tmpName)
	if ingress.Kind == AttachmentImage {
		if detectErr != nil || !strings.HasPrefix(strings.ToLower(detectedType), "image/") {
			_ = os.Remove(tmpName)
			if detectErr != nil {
				return SessionAttachment{}, fmt.Errorf("detect image attachment: %w", detectErr)
			}
			return SessionAttachment{}, fmt.Errorf("image attachment has detected media type %q", detectedType)
		}
		// Use the bytes-derived type for image input instead of trusting an
		// event's filename or content-type hint.
		mediaType = detectedType
	} else if mediaType == "" && detectErr == nil {
		mediaType = detectedType
	}

	storageKey := filepath.ToSlash(filepath.Join("attachments", sessionID, attachmentID, "content"))
	finalPath := filepath.Join(dir, "content")
	if err := os.Rename(tmpName, finalPath); err != nil {
		_ = os.Remove(tmpName)
		return SessionAttachment{}, fmt.Errorf("commit attachment: %w", err)
	}

	now := time.Now().UTC()
	record := SessionAttachment{
		ID: attachmentID, SessionID: sessionID, RunID: runID,
		Origin: ingress.Origin, Kind: ingress.Kind, Filename: filename,
		MediaType: mediaType, Bytes: written, SHA256: hex.EncodeToString(hash.Sum(nil)),
		StorageKey: storageKey, Status: "accepted", CreatedAt: now,
		ExpiresAt: now.Add(s.policy.Retention),
	}
	if err := session.WriteRootDatabase(ctx, s.sessionDir, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO session_attachments
			(id, session_id, run_id, origin, kind, filename, media_type, byte_size,
			 sha256, storage_key, status, created_at, expires_at, metadata)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}')`,
			record.ID, record.SessionID, record.RunID, record.Origin,
			string(record.Kind), record.Filename, record.MediaType, record.Bytes,
			record.SHA256, record.StorageKey, record.Status,
			record.CreatedAt.Format(time.RFC3339Nano), record.ExpiresAt.Format(time.RFC3339Nano))
		return err
	}); err != nil {
		_ = os.Remove(finalPath)
		return SessionAttachment{}, fmt.Errorf("persist attachment: %w", err)
	}
	return record, nil
}

// Get returns one attachment record belonging to sessionID.
func (s *AttachmentService) Get(ctx context.Context, sessionID, attachmentID string) (SessionAttachment, error) {
	if s == nil {
		return SessionAttachment{}, fmt.Errorf("attachment service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validatePathComponent(sessionID); err != nil {
		return SessionAttachment{}, err
	}
	if err := validatePathComponent(attachmentID); err != nil {
		return SessionAttachment{}, err
	}
	var record SessionAttachment
	err := session.QueryRootDatabase(s.sessionDir, func(db *sql.DB) error {
		var kind, createdAt, expiresAt string
		err := db.QueryRowContext(ctx, `SELECT id, session_id, run_id, origin, kind,
			filename, media_type, byte_size, sha256, storage_key, status,
			created_at, expires_at
			FROM session_attachments WHERE session_id = ? AND id = ?`, sessionID, attachmentID).
			Scan(&record.ID, &record.SessionID, &record.RunID, &record.Origin, &kind,
				&record.Filename, &record.MediaType, &record.Bytes, &record.SHA256,
				&record.StorageKey, &record.Status, &createdAt, &expiresAt)
		if err != nil {
			return err
		}
		record.Kind = AttachmentKind(kind)
		record.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		record.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresAt)
		return nil
	})
	if err != nil {
		return SessionAttachment{}, err
	}
	return record, nil
}

// Open returns the private content stream after checking session ownership and
// expiry. Callers must close the returned reader.
func (s *AttachmentService) Open(ctx context.Context, sessionID, attachmentID string) (SessionAttachment, io.ReadCloser, error) {
	record, err := s.Get(ctx, sessionID, attachmentID)
	if err != nil {
		return SessionAttachment{}, nil, err
	}
	if !record.ExpiresAt.IsZero() && time.Now().After(record.ExpiresAt) {
		return record, nil, fmt.Errorf("attachment %s has expired", attachmentID)
	}
	path, err := s.storagePath(record.StorageKey)
	if err != nil {
		return record, nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return record, nil, fmt.Errorf("open attachment: %w", err)
	}
	return record, file, nil
}

// CleanupExpired expires and removes private attachment content whose TTL has
// elapsed. It is deliberately tolerant of an already-missing file: expiry is
// a durable Runtime state, while a subsequent invocation can retry a failed
// filesystem removal without involving any transport adapter.
func (s *AttachmentService) CleanupExpired(ctx context.Context) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("attachment service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	type expiredAttachment struct {
		id         string
		storageKey string
	}
	var expired []expiredAttachment
	err := session.WriteRootDatabase(ctx, s.sessionDir, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, storage_key FROM session_attachments WHERE expires_at <= ?`, now.Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item expiredAttachment
			if err := rows.Scan(&item.id, &item.storageKey); err != nil {
				return err
			}
			expired = append(expired, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE session_attachments SET status = 'expired' WHERE expires_at <= ? AND status != 'expired'`, now.Format(time.RFC3339Nano))
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("expire attachments: %w", err)
	}
	for _, item := range expired {
		path, err := s.storagePath(item.storageKey)
		if err != nil {
			return len(expired), err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return len(expired), fmt.Errorf("remove expired attachment %s: %w", item.id, err)
		}
		// The attachment directory contains only the random content object once
		// Accept has committed it. Ignore a non-empty/missing parent so a retry
		// remains safe if a process was interrupted around the original write.
		_ = os.Remove(filepath.Dir(path))
	}
	return len(expired), nil
}

func (s *AttachmentService) storagePath(storageKey string) (string, error) {
	if strings.TrimSpace(storageKey) == "" {
		return "", fmt.Errorf("invalid attachment storage key")
	}
	path := filepath.Join(s.sessionDir, filepath.FromSlash(storageKey))
	rel, err := filepath.Rel(s.sessionDir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid attachment storage key")
	}
	return path, nil
}

// SetStatus updates the Runtime-owned lifecycle state of an attachment. It is
// used for explicit state transitions such as accepted -> generated -> expired;
// adapters do not write attachment rows or statuses directly.
func (s *AttachmentService) SetStatus(ctx context.Context, sessionID, attachmentID, status string) error {
	if s == nil {
		return fmt.Errorf("attachment service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validatePathComponent(sessionID); err != nil {
		return err
	}
	if err := validatePathComponent(attachmentID); err != nil {
		return err
	}
	if status != "accepted" && status != "generated" && status != "expired" {
		return fmt.Errorf("invalid attachment status %q", status)
	}
	return session.WriteRootDatabase(ctx, s.sessionDir, func(tx *sql.Tx) error {
		result, err := tx.Exec(`UPDATE session_attachments SET status = ? WHERE session_id = ? AND id = ?`, status, sessionID, attachmentID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return fmt.Errorf("attachment %s not found for session", attachmentID)
		}
		return nil
	})
}

func (r SessionAttachment) Input() InputAttachment {
	return InputAttachment{
		AttachmentID: r.ID,
		Kind:         r.Kind,
		Filename:     r.Filename,
		MediaType:    r.MediaType,
		Bytes:        r.Bytes,
	}
}

// AcceptInput persists every ephemeral ingress stream and returns the only
// input shape that may subsequently enter an Agent run. Callers keep protocol
// parsing outside Runtime, but never download to a working directory or build
// provider blocks themselves.
func (r *SessionRuntime) AcceptInput(ctx context.Context, runID, text string, ingresses []AttachmentIngress) (RunInput, error) {
	if err := r.ensureOpen(); err != nil {
		return RunInput{}, err
	}
	// Plain text has no external resource to persist. Keeping this normalization
	// at the Runtime boundary lets every adapter use one input entrypoint even
	// for transient/no-session runs.
	if len(ingresses) == 0 {
		return RunInput{Text: text}, nil
	}
	r.mu.RLock()
	attachments := r.Attachments
	manager := r.Manager
	sessionID := r.ID
	r.mu.RUnlock()
	if attachments == nil || manager == nil || sessionID == "" {
		return RunInput{}, fmt.Errorf("attachment runtime is not bound to a session")
	}
	input := RunInput{Text: text, Attachments: make([]InputAttachment, 0, len(ingresses))}
	for _, ingress := range ingresses {
		record, err := attachments.Accept(ctx, sessionID, runID, ingress)
		if err != nil {
			return RunInput{}, err
		}
		input.Attachments = append(input.Attachments, record.Input())
	}
	return input, nil
}

// AcceptProviderAttachment materializes a provider-declared output attachment
// into the same private session store used for inbound media. A URL or a
// filename alone is never an artifact: the provider must expose an authorized
// resolver for its opaque reference.
func (r *SessionRuntime) AcceptProviderAttachment(ctx context.Context, runID string, p provider.Provider, attachment provider.Attachment) (SessionAttachment, error) {
	if err := r.ensureOpen(); err != nil {
		return SessionAttachment{}, err
	}
	if p == nil {
		return SessionAttachment{}, fmt.Errorf("provider attachment resolver is required")
	}
	if err := provider.ValidateAttachmentReferenceForResolver(attachment.ProviderRef); err != nil {
		return SessionAttachment{}, err
	}
	kind := AttachmentKind(attachment.Kind)
	if kind != AttachmentImage && kind != AttachmentFile {
		return SessionAttachment{}, fmt.Errorf("provider attachment kind %q is not deliverable", attachment.Kind)
	}
	var (
		content provider.AttachmentContent
		err     error
	)
	if resolver, ok := p.(provider.AttachmentMetadataResolver); ok {
		content, err = resolver.ResolveAttachmentWithMetadata(ctx, attachment)
	} else if resolver, ok := p.(provider.AttachmentResolver); ok {
		content, err = resolver.ResolveAttachment(ctx, attachment.ProviderRef)
	} else {
		return SessionAttachment{}, fmt.Errorf("provider %q cannot resolve attachments", p.Name())
	}
	if err != nil {
		return SessionAttachment{}, fmt.Errorf("resolve provider attachment: %w", err)
	}
	if len(content.Data) == 0 {
		return SessionAttachment{}, fmt.Errorf("provider attachment is empty")
	}
	r.mu.RLock()
	service := r.Attachments
	sessionID := r.ID
	r.mu.RUnlock()
	if service == nil || sessionID == "" {
		return SessionAttachment{}, fmt.Errorf("attachment runtime is not bound to a session")
	}
	filename := content.Filename
	if filename == "" {
		filename = attachment.Name
	}
	mediaType := content.MediaType
	if mediaType == "" {
		mediaType = attachment.MediaType
	}
	record, err := service.Accept(ctx, sessionID, runID, AttachmentIngress{
		Origin: "provider:" + p.Name(), Reference: attachment.ProviderRef, Kind: kind,
		Filename: filename, MediaType: mediaType, SizeHint: int64(len(content.Data)),
		Open: func(context.Context) (AttachmentStream, error) {
			return AttachmentStream{Reader: io.NopCloser(bytes.NewReader(content.Data)), Filename: filename, MediaType: mediaType, ContentSize: int64(len(content.Data))}, nil
		},
	})
	if err != nil {
		return SessionAttachment{}, err
	}
	if err := service.SetStatus(ctx, sessionID, record.ID, "generated"); err != nil {
		return SessionAttachment{}, err
	}
	record.Status = "generated"
	return record, nil
}

// BuildUserMessage resolves an accepted RunInput into the Agent Core's single
// provider message. Provider-specific wire encoding remains in provider
// implementations; no frontend adapter is permitted to construct rich blocks
// directly from a platform download.
func (r *SessionRuntime) BuildUserMessage(ctx context.Context, input RunInput) (provider.Message, error) {
	if err := r.ensureOpen(); err != nil {
		return provider.Message{}, err
	}
	r.mu.RLock()
	attachments := r.Attachments
	registry := r.Registry
	sessionID := r.ID
	r.mu.RUnlock()
	if len(input.Attachments) == 0 {
		if registry != nil {
			registry.Remove("read_attachment")
		}
		return provider.NewUserMessage(input.Text), nil
	}
	if attachments == nil || sessionID == "" {
		return provider.Message{}, fmt.Errorf("attachment runtime is not bound to a session")
	}

	contents := make([]provider.ContentBlock, 0, len(input.Attachments)+1)
	text := strings.TrimSpace(input.Text)
	fileRecords := make([]SessionAttachment, 0, len(input.Attachments))
	for _, item := range input.Attachments {
		record, err := attachments.Get(ctx, sessionID, item.AttachmentID)
		if err != nil {
			return provider.Message{}, fmt.Errorf("resolve attachment %s: %w", item.AttachmentID, err)
		}
		if record.Kind != item.Kind {
			return provider.Message{}, fmt.Errorf("attachment %s kind mismatch", item.AttachmentID)
		}
		switch record.Kind {
		case AttachmentImage:
			_, reader, err := attachments.Open(ctx, sessionID, record.ID)
			if err != nil {
				return provider.Message{}, err
			}
			data, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if readErr != nil {
				return provider.Message{}, fmt.Errorf("read image attachment %s: %w", record.ID, readErr)
			}
			if closeErr != nil {
				return provider.Message{}, fmt.Errorf("close image attachment %s: %w", record.ID, closeErr)
			}
			contents = append(contents, provider.ContentBlock{Type: "image", Image: &provider.ImageContent{
				MimeType: record.MediaType,
				Data:     base64.StdEncoding.EncodeToString(data),
				Bytes:    len(data),
			}})
		case AttachmentFile:
			fileRecords = append(fileRecords, record)
		default:
			return provider.Message{}, fmt.Errorf("attachment %s has unsupported kind %q", record.ID, record.Kind)
		}
	}
	if len(fileRecords) > 0 {
		manifest := buildAttachmentManifest(fileRecords)
		if text != "" {
			text += "\n\n" + manifest
		} else {
			text = manifest
		}
		if registry != nil {
			registry.Register(NewReadAttachmentTool(attachments, sessionID, fileRecords))
		}
	} else if registry != nil {
		registry.Remove("read_attachment")
	}
	if text != "" {
		contents = append([]provider.ContentBlock{{Type: "text", Text: text}}, contents...)
	}
	if len(contents) == 0 {
		return provider.Message{}, fmt.Errorf("run input must contain text or attachments")
	}
	msg := provider.NewUserMessage(text)
	msg.Contents = contents
	return msg, nil
}

// ValidateRunInput applies the provider/model-independent capability contract
// before a durable Run is admitted. Image bytes are retained even when the
// selected model cannot see them, but the request fails explicitly instead of
// silently turning an image into an empty prompt.
func ValidateRunInput(model *provider.Model, input RunInput) error {
	for _, attachment := range input.Attachments {
		if attachment.Kind != AttachmentImage {
			continue
		}
		if modelSupportsRunInput(model, "image") {
			continue
		}
		modelID := ""
		if model != nil {
			modelID = model.ID
		}
		return fmt.Errorf("model %q does not support image input", modelID)
	}
	return nil
}

func modelSupportsRunInput(model *provider.Model, input string) bool {
	if model == nil {
		return false
	}
	for _, item := range model.Input {
		if item == input {
			return true
		}
	}
	return false
}

func buildAttachmentManifest(records []SessionAttachment) string {
	var b strings.Builder
	b.WriteString("[Runtime-managed file attachments. Use read_attachment with attachmentId; do not infer or request local paths.]\n")
	for _, record := range records {
		fmt.Fprintf(&b, "- id=%s; name=%q; mediaType=%q; bytes=%d\n", record.ID, record.Filename, record.MediaType, record.Bytes)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func validatePathComponent(value string) error {
	if value == "" || value == "." || value == ".." || filepath.Base(value) != value || strings.ContainsRune(value, filepath.Separator) {
		return fmt.Errorf("invalid attachment path component")
	}
	return nil
}

func sanitizeAttachmentFilename(value string) string {
	value = filepath.Base(strings.TrimSpace(value))
	if value == "." || value == ".." || value == "" {
		return "attachment"
	}
	var b strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 255 {
			break
		}
	}
	if b.Len() == 0 {
		return "attachment"
	}
	return b.String()
}

func detectAttachmentMediaType(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return "", err
	}
	return http.DetectContentType(buf[:n]), nil
}
