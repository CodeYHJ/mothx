package agentruntime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/session"
	_ "golang.org/x/image/webp"
)

// InputIngress is the ephemeral adapter-to-Runtime handoff for one input
// resource. Reference and transport credentials are never persisted.
type InputIngress struct {
	Origin        string
	EventID       string
	ItemIndex     int
	Reference     string
	Kind          AttachmentKind
	FilenameHint  string
	MediaTypeHint string
	SizeHint      int64
	Open          func(context.Context) (InputStream, error)
}

// InputStream is the authenticated one-shot stream supplied by an adapter.
type InputStream struct {
	Reader      io.ReadCloser
	Filename    string
	MediaType   string
	ContentSize int64
}

// PreparedInput is the opaque resource reference carried by an input
// submission after Runtime has materialized and persisted it.
type PreparedInput struct {
	ResourceID   string
	Kind         AttachmentKind
	RelativePath string
	Filename     string
	MediaType    string
	Bytes        int64
}

// InputSubmission is the only user-input contract consumed by SessionRuntime.
// IdempotencyKey carries the caller's submission identity. Resource item
// idempotency is enforced here; durable submission reservation and existing-Run
// reuse are handled by the Runtime admission layer.
type InputSubmission struct {
	Text           string
	Resources      []PreparedInput
	IdempotencyKey string
}

// ResourceIDs returns the canonical Runtime resource IDs in submission order.
// The slice is copied so callers cannot mutate the submission's ownership
// facts while durable admission is assembling its transaction.
func (s InputSubmission) ResourceIDs() []string {
	ids := make([]string, 0, len(s.Resources))
	for _, resource := range s.Resources {
		if resource.ResourceID != "" {
			ids = append(ids, resource.ResourceID)
		}
	}
	return ids
}

// RunInput remains a source-compatible name while adapters migrate to the
// canonical InputSubmission name. It is an alias, not a second input model.
type RunInput = InputSubmission

// InputResource is the canonical persisted input file record.
type InputResource struct {
	ID           string
	SessionID    string
	RunID        string
	Origin       string
	EventID      string
	ItemIndex    int
	ItemKey      string
	Kind         AttachmentKind
	Filename     string
	MediaType    string
	Bytes        int64
	SHA256       string
	RelativePath string
	Status       string
	CreatedAt    time.Time
}

func (r InputResource) Prepared() PreparedInput {
	return PreparedInput{
		ResourceID: r.ID, Kind: r.Kind, RelativePath: r.RelativePath,
		Filename: r.Filename, MediaType: r.MediaType, Bytes: r.Bytes,
	}
}

// InputPolicy contains reliability limits applied before an input file enters
// the project workspace. It does not select file value or parse documents.
type InputPolicy struct {
	MaxImageBytes  int64
	MaxFileBytes   int64
	MaxImagePixels int64
}

func DefaultInputPolicy() InputPolicy {
	return InputPolicy{MaxImageBytes: 20 << 20, MaxFileBytes: 50 << 20, MaxImagePixels: 40_000_000}
}

// InputMaterializer owns project-relative input files and their session-backed
// records. Artifact bytes deliberately use a different private store.
type InputMaterializer struct {
	sessionDir string
	workDir    string
	policy     InputPolicy
}

func NewInputMaterializer(sessionDir, workDir string, policy InputPolicy) (*InputMaterializer, error) {
	if strings.TrimSpace(sessionDir) == "" {
		return nil, fmt.Errorf("input session directory is required")
	}
	if strings.TrimSpace(workDir) == "" {
		return nil, fmt.Errorf("input work directory is required")
	}
	if policy.MaxImageBytes <= 0 || policy.MaxFileBytes <= 0 || policy.MaxImagePixels <= 0 {
		return nil, fmt.Errorf("input resource limits must be positive")
	}
	return &InputMaterializer{
		sessionDir: filepath.Clean(sessionDir), workDir: filepath.Clean(workDir), policy: policy,
	}, nil
}

// Prepare streams one resource into .mothx/tmp/inputs and persists its
// canonical metadata. Stable platform items are idempotent across retries and
// concurrent deliveries.
func (m *InputMaterializer) Prepare(ctx context.Context, sessionID, runID string, ingress InputIngress) (InputResource, error) {
	if m == nil {
		return InputResource{}, fmt.Errorf("input materializer is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validatePathComponent(sessionID); err != nil {
		return InputResource{}, err
	}
	if ingress.Open == nil {
		return InputResource{}, fmt.Errorf("input source is not readable")
	}
	if ingress.Kind != AttachmentImage && ingress.Kind != AttachmentFile {
		return InputResource{}, fmt.Errorf("unsupported input kind %q", ingress.Kind)
	}
	itemKey := inputItemKey(ingress)
	if itemKey != "" {
		existing, err := m.getByItemKey(ctx, sessionID, itemKey)
		if err == nil {
			return existing, nil
		}
		if err != sql.ErrNoRows {
			return InputResource{}, err
		}
	}

	maxBytes := m.policy.MaxFileBytes
	if ingress.Kind == AttachmentImage {
		maxBytes = m.policy.MaxImageBytes
	}
	if ingress.SizeHint > maxBytes {
		return InputResource{}, fmt.Errorf("input exceeds %d bytes", maxBytes)
	}

	root, err := m.inputRoot()
	if err != nil {
		return InputResource{}, err
	}
	resourceID := session.GenerateID()
	if err := validatePathComponent(resourceID); err != nil {
		return InputResource{}, err
	}
	dir := filepath.Join(root, resourceID)
	if err := os.Mkdir(dir, 0700); err != nil {
		return InputResource{}, fmt.Errorf("create input resource directory: %w", err)
	}
	removeResource := func() { _ = os.RemoveAll(dir) }
	tmp, err := os.CreateTemp(dir, ".incoming-*")
	if err != nil {
		removeResource()
		return InputResource{}, fmt.Errorf("create input temporary file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		removeResource()
	}

	stream, err := ingress.Open(ctx)
	if err != nil {
		cleanup()
		return InputResource{}, fmt.Errorf("open %s input: %w", ingress.Origin, err)
	}
	if stream.Reader == nil {
		cleanup()
		return InputResource{}, fmt.Errorf("open %s input: empty reader", ingress.Origin)
	}
	defer stream.Reader.Close()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(stream.Reader, maxBytes+1))
	if err != nil {
		cleanup()
		return InputResource{}, fmt.Errorf("read input: %w", err)
	}
	if written > maxBytes {
		cleanup()
		return InputResource{}, fmt.Errorf("input exceeds %d bytes", maxBytes)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return InputResource{}, fmt.Errorf("sync input: %w", err)
	}
	if err := tmp.Close(); err != nil {
		removeResource()
		return InputResource{}, fmt.Errorf("close input: %w", err)
	}

	detectedType, err := detectInputMediaType(tmpName)
	if err != nil {
		removeResource()
		return InputResource{}, fmt.Errorf("detect input media type: %w", err)
	}
	mediaType := strings.TrimSpace(ingress.MediaTypeHint)
	if ingress.Kind == AttachmentImage {
		if !strings.HasPrefix(strings.ToLower(detectedType), "image/") {
			removeResource()
			return InputResource{}, fmt.Errorf("image input has detected media type %q", detectedType)
		}
		if err := m.inspectImage(tmpName); err != nil {
			removeResource()
			return InputResource{}, err
		}
		mediaType = detectedType
	} else if mediaType == "" {
		mediaType = detectedType
	}
	filename := strings.TrimSpace(ingress.FilenameHint)
	if filename == "" {
		filename = strings.TrimSpace(stream.Filename)
	}
	filename = canonicalInputFilename(filename, mediaType)
	finalPath := filepath.Join(dir, filename)
	if err := os.Rename(tmpName, finalPath); err != nil {
		removeResource()
		return InputResource{}, fmt.Errorf("commit input: %w", err)
	}
	relativePath, err := filepath.Rel(m.workDir, finalPath)
	if err != nil || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		removeResource()
		return InputResource{}, fmt.Errorf("input path escaped Runtime work directory")
	}
	now := time.Now().UTC()
	// The resource row is intentionally staged first. Durable admission binds
	// it to a Run in the same transaction as the intent, Run row, turn boundary,
	// and started event; a failed admission therefore cannot leave an attached
	// resource pointing at a nonexistent Run.
	status := "prepared"
	record := InputResource{
		ID: resourceID, SessionID: sessionID, RunID: "", Origin: strings.TrimSpace(ingress.Origin),
		EventID: strings.TrimSpace(ingress.EventID), ItemIndex: ingress.ItemIndex, ItemKey: itemKey,
		Kind: ingress.Kind, Filename: filename, MediaType: mediaType, Bytes: written,
		SHA256: hex.EncodeToString(hash.Sum(nil)), RelativePath: filepath.ToSlash(relativePath),
		Status: status, CreatedAt: now,
	}
	err = session.WriteRootDatabase(ctx, m.sessionDir, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO input_resources
			(id, session_id, run_id, origin, event_id, item_index, item_key, kind,
			 filename, media_type, byte_size, sha256, relative_path, status, created_at, metadata)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}')`,
			record.ID, record.SessionID, record.RunID, record.Origin, record.EventID,
			record.ItemIndex, record.ItemKey, string(record.Kind), record.Filename,
			record.MediaType, record.Bytes, record.SHA256, record.RelativePath,
			record.Status, record.CreatedAt.Format(time.RFC3339Nano))
		return err
	})
	if err == nil {
		return record, nil
	}
	removeResource()
	if itemKey != "" {
		existing, lookupErr := m.getByItemKey(ctx, sessionID, itemKey)
		if lookupErr == nil {
			return existing, nil
		}
	}
	return InputResource{}, fmt.Errorf("persist input resource: %w", err)
}

func (m *InputMaterializer) Get(ctx context.Context, sessionID, resourceID string) (InputResource, error) {
	if m == nil {
		return InputResource{}, fmt.Errorf("input materializer is nil")
	}
	if err := validatePathComponent(sessionID); err != nil {
		return InputResource{}, err
	}
	if err := validatePathComponent(resourceID); err != nil {
		return InputResource{}, err
	}
	var record InputResource
	err := session.QueryRootDatabase(m.sessionDir, func(db *sql.DB) error {
		return scanInputResource(db.QueryRowContext(ctx, `SELECT id, session_id, run_id, origin,
			event_id, item_index, item_key, kind, filename, media_type, byte_size,
			sha256, relative_path, status, created_at
			FROM input_resources WHERE session_id = ? AND id = ?`, sessionID, resourceID), &record)
	})
	return record, err
}

func (m *InputMaterializer) getByItemKey(ctx context.Context, sessionID, itemKey string) (InputResource, error) {
	var record InputResource
	err := session.QueryRootDatabase(m.sessionDir, func(db *sql.DB) error {
		return scanInputResource(db.QueryRowContext(ctx, `SELECT id, session_id, run_id, origin,
			event_id, item_index, item_key, kind, filename, media_type, byte_size,
			sha256, relative_path, status, created_at
			FROM input_resources WHERE session_id = ? AND item_key = ?`, sessionID, itemKey), &record)
	})
	return record, err
}

type rowScanner interface {
	Scan(...any) error
}

func scanInputResource(row rowScanner, record *InputResource) error {
	var kind, createdAt string
	if err := row.Scan(&record.ID, &record.SessionID, &record.RunID, &record.Origin,
		&record.EventID, &record.ItemIndex, &record.ItemKey, &kind, &record.Filename,
		&record.MediaType, &record.Bytes, &record.SHA256, &record.RelativePath,
		&record.Status, &createdAt); err != nil {
		return err
	}
	record.Kind = AttachmentKind(kind)
	record.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return nil
}

func (m *InputMaterializer) inputRoot() (string, error) {
	workDir, err := filepath.EvalSymlinks(m.workDir)
	if err != nil {
		return "", fmt.Errorf("resolve input work directory: %w", err)
	}
	root := filepath.Join(workDir, ".mothx", "tmp", "inputs")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", fmt.Errorf("create input root: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve input root: %w", err)
	}
	rel, err := filepath.Rel(workDir, resolvedRoot)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("input root escaped Runtime work directory")
	}
	return resolvedRoot, nil
}

func (m *InputMaterializer) resourcePath(relativePath string) (string, error) {
	if strings.TrimSpace(relativePath) == "" || filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("invalid input relative path")
	}
	path := filepath.Join(m.workDir, filepath.FromSlash(relativePath))
	rel, err := filepath.Rel(m.workDir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid input relative path")
	}
	return path, nil
}

func (m *InputMaterializer) inspectImage(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("inspect image input: %w", err)
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return fmt.Errorf("inspect image input: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return fmt.Errorf("inspect image input: invalid dimensions %dx%d", cfg.Width, cfg.Height)
	}
	pixels := int64(cfg.Width) * int64(cfg.Height)
	if pixels > m.policy.MaxImagePixels {
		return fmt.Errorf("image input has %d pixels (max %d)", pixels, m.policy.MaxImagePixels)
	}
	return nil
}

func inputItemKey(ingress InputIngress) string {
	eventID := strings.TrimSpace(ingress.EventID)
	if eventID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(ingress.Origin) + "\x00" + eventID + "\x00" + strconv.Itoa(ingress.ItemIndex)))
	return hex.EncodeToString(sum[:])
}

func canonicalInputFilename(filename, mediaType string) string {
	filename = sanitizeAttachmentFilename(filename)
	extension := canonicalImageExtension(mediaType)
	if extension == "" {
		return filename
	}
	current := strings.ToLower(filepath.Ext(filename))
	if current == extension || (extension == ".jpg" && current == ".jpeg") {
		return filename
	}
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	if base == "" || base == "." {
		base = "image"
	}
	return base + extension
}

func canonicalImageExtension(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mediaType, ";")[0])) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}

func detectInputMediaType(path string) (string, error) {
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
	detected := http.DetectContentType(buf[:n])
	if detected != "application/octet-stream" && detected != "application/zip" {
		return detected, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return detected, nil
	}
	if _, format, decodeErr := image.DecodeConfig(f); decodeErr == nil {
		switch strings.ToLower(format) {
		case "jpeg":
			return "image/jpeg", nil
		case "png":
			return "image/png", nil
		case "gif":
			return "image/gif", nil
		case "webp":
			return "image/webp", nil
		}
	}
	return detected, nil
}

// AcceptInput materializes every ephemeral resource and returns the canonical
// submission shape consumed by Agent Core.
func (r *SessionRuntime) AcceptInput(ctx context.Context, runID, text string, ingresses []InputIngress) (InputSubmission, error) {
	if err := r.ensureOpen(); err != nil {
		return InputSubmission{}, err
	}
	if len(ingresses) == 0 {
		return InputSubmission{Text: text}, nil
	}
	r.mu.RLock()
	inputs := r.Inputs
	sessionID := r.ID
	r.mu.RUnlock()
	if inputs == nil || sessionID == "" {
		return InputSubmission{}, fmt.Errorf("input materializer is not bound to a session")
	}
	submission := InputSubmission{Text: text, Resources: make([]PreparedInput, 0, len(ingresses))}
	for index, ingress := range ingresses {
		if ingress.EventID == "" && runID != "" {
			ingress.EventID = runID
			ingress.ItemIndex = index
		}
		record, err := inputs.Prepare(ctx, sessionID, runID, ingress)
		if err != nil {
			return InputSubmission{}, err
		}
		submission.Resources = append(submission.Resources, record.Prepared())
	}
	return submission, nil
}

// BuildUserMessage emits only text plus a deterministic project-path manifest.
// It never reads input bytes or constructs provider image/file blocks.
func (r *SessionRuntime) BuildUserMessage(ctx context.Context, input InputSubmission) (provider.Message, error) {
	if err := r.ensureOpen(); err != nil {
		return provider.Message{}, err
	}
	if len(input.Resources) == 0 {
		return provider.NewUserMessage(input.Text), nil
	}
	r.mu.RLock()
	inputs := r.Inputs
	sessionID := r.ID
	r.mu.RUnlock()
	if inputs == nil || sessionID == "" {
		return provider.Message{}, fmt.Errorf("input materializer is not bound to a session")
	}
	records := make([]InputResource, 0, len(input.Resources))
	for _, prepared := range input.Resources {
		record, err := inputs.Get(ctx, sessionID, prepared.ResourceID)
		if err != nil {
			return provider.Message{}, fmt.Errorf("resolve input resource %s: %w", prepared.ResourceID, err)
		}
		records = append(records, record)
	}
	text := strings.TrimSpace(input.Text)
	manifest := inputs.buildManifest(records)
	if text != "" {
		text += "\n\n" + manifest
	} else {
		text = manifest
	}
	return provider.NewUserMessage(text), nil
}

func (m *InputMaterializer) buildManifest(records []InputResource) string {
	var b strings.Builder
	b.WriteString("[Runtime-managed input files for this request]\n")
	for _, record := range records {
		status := "available"
		path, err := m.resourcePath(record.RelativePath)
		if err != nil {
			status = "invalid"
		} else if info, statErr := os.Stat(path); statErr != nil || !info.Mode().IsRegular() {
			status = "missing"
		}
		fmt.Fprintf(&b, "- path: %s\n  name: %s\n  mediaType: %s\n  bytes: %d\n  status: %s\n",
			record.RelativePath, record.Filename, record.MediaType, record.Bytes, status)
	}
	b.WriteString("\nDecide whether a file needs inspection. Use read for files you choose to read,\n")
	b.WriteString("or use an appropriate available Skill/tool when specialized parsing is useful.\n")
	b.WriteString("Do not claim to have examined a file that you did not read.")
	return b.String()
}
