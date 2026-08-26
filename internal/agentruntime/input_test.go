package agentruntime

import (
	"context"
	"database/sql"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/commondb"
	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/session"
	"github.com/startvibecoding/mothx/internal/tools"
)

func TestAttachmentServiceAcceptsAndReopensSessionAttachment(t *testing.T) {
	root := t.TempDir()
	mgr := session.New(t.TempDir(), root)
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	service, err := NewAttachmentService(root, AttachmentPolicy{
		MaxImageBytes: 1 << 20,
		MaxFileBytes:  1 << 20,
		Retention:     time.Hour,
	})
	if err != nil {
		t.Fatalf("new attachment service: %v", err)
	}

	record, err := service.Accept(context.Background(), mgr.GetHeader().ID, "run-1", AttachmentIngress{
		Origin: "test", Reference: "opaque-1", MessageID: "message-1",
		Kind: AttachmentFile, Filename: "notes.txt", MediaType: "text/plain",
		Open: func(context.Context) (AttachmentStream, error) {
			return AttachmentStream{Reader: io.NopCloser(strings.NewReader("hello attachment"))}, nil
		},
	})
	if err != nil {
		t.Fatalf("accept attachment: %v", err)
	}
	if record.ID == "" || record.StorageKey == "" || record.Status != "accepted" {
		t.Fatalf("unexpected record: %#v", record)
	}
	if record.Bytes != int64(len("hello attachment")) {
		t.Fatalf("attachment bytes = %d", record.Bytes)
	}

	got, reader, err := service.Open(context.Background(), mgr.GetHeader().ID, record.ID)
	if err != nil {
		t.Fatalf("open attachment: %v", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	if string(data) != "hello attachment" {
		t.Fatalf("attachment content = %q", data)
	}
	if got.SHA256 != record.SHA256 || got.SessionID != record.SessionID || got.RunID != record.RunID {
		t.Fatalf("reopened record = %#v, want %#v", got, record)
	}
	if input := record.Input(); input.AttachmentID != record.ID || input.Filename != "notes.txt" {
		t.Fatalf("input attachment = %#v", input)
	}
	_ = commondb.CloseAll()
}

func TestBuildUserMessageUsesRuntimeAttachmentReferences(t *testing.T) {
	root := t.TempDir()
	mgr := session.New(t.TempDir(), root)
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	service, err := NewAttachmentService(root, DefaultAttachmentPolicy())
	if err != nil {
		t.Fatalf("new attachment service: %v", err)
	}
	record, err := service.Accept(context.Background(), mgr.GetHeader().ID, "run-1", AttachmentIngress{
		Origin: "test", Kind: AttachmentFile, Filename: "request.txt", MediaType: "text/plain",
		Open: func(context.Context) (AttachmentStream, error) {
			return AttachmentStream{Reader: io.NopCloser(strings.NewReader("attachment body"))}, nil
		},
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	runtime := &SessionRuntime{ID: mgr.GetHeader().ID, Attachments: service, Registry: tools.NewRegistry(t.TempDir(), nil)}
	msg, err := runtime.BuildUserMessage(context.Background(), RunInput{Text: "inspect this", Attachments: []InputAttachment{record.Input()}})
	if err != nil {
		t.Fatalf("build user message: %v", err)
	}
	if !strings.Contains(msg.Content, "read_attachment") || !strings.Contains(msg.Content, record.ID) {
		t.Fatalf("message manifest = %q", msg.Content)
	}
	tool, ok := runtime.Registry.Get("read_attachment")
	if !ok {
		t.Fatal("read_attachment tool was not registered")
	}
	result, err := tool.Execute(context.Background(), map[string]any{"attachmentId": record.ID})
	if err != nil || result.Text != "attachment body" {
		t.Fatalf("read attachment result = %#v, %v", result, err)
	}
	_ = commondb.CloseAll()
}

func TestBuildUserMessageUsesAcceptedImageAndValidatesCapabilities(t *testing.T) {
	root := t.TempDir()
	mgr := session.New(t.TempDir(), root)
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	service, err := NewAttachmentService(root, DefaultAttachmentPolicy())
	if err != nil {
		t.Fatalf("new attachment service: %v", err)
	}
	// Valid one-pixel PNG. The service intentionally derives image/png from
	// bytes rather than trusting the supplied MIME hint.
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl8P6sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Accept(context.Background(), mgr.GetHeader().ID, "run-1", AttachmentIngress{
		Origin: "test", Kind: AttachmentImage, Filename: "screen.bin", MediaType: "application/octet-stream",
		Open: func(context.Context) (AttachmentStream, error) {
			return AttachmentStream{Reader: io.NopCloser(strings.NewReader(string(png)))}, nil
		},
	})
	if err != nil {
		t.Fatalf("accept image: %v", err)
	}
	runtime := &SessionRuntime{ID: mgr.GetHeader().ID, Attachments: service, Registry: tools.NewRegistry(t.TempDir(), nil)}
	input := RunInput{Attachments: []InputAttachment{record.Input()}}
	msg, err := runtime.BuildUserMessage(context.Background(), input)
	if err != nil {
		t.Fatalf("build image message: %v", err)
	}
	if len(msg.Contents) != 1 || msg.Contents[0].Image == nil || msg.Contents[0].Image.MimeType != "image/png" {
		t.Fatalf("image message = %#v", msg.Contents)
	}
	if err := ValidateRunInput(&provider.Model{ID: "text-only", Input: []string{"text"}}, input); err == nil {
		t.Fatal("text-only model accepted image input")
	}
	if err := ValidateRunInput(&provider.Model{ID: "vision", Input: []string{"text", "image"}}, input); err != nil {
		t.Fatalf("vision model rejected image input: %v", err)
	}
	_ = commondb.CloseAll()
}

func TestAcceptProviderAttachmentMaterializesGeneratedArtifact(t *testing.T) {
	root := t.TempDir()
	mgr := session.New(t.TempDir(), root)
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	service, err := NewAttachmentService(root, DefaultAttachmentPolicy())
	if err != nil {
		t.Fatalf("new attachment service: %v", err)
	}
	runtime := &SessionRuntime{ID: mgr.GetHeader().ID, Manager: mgr, Attachments: service}
	record, err := runtime.AcceptProviderAttachment(context.Background(), "run-output", attachmentResolverProvider{}, provider.Attachment{
		Kind: "file", Name: "report.txt", MediaType: "text/plain", ProviderRef: "file_output_1",
	})
	if err != nil {
		t.Fatalf("accept provider attachment: %v", err)
	}
	if record.Status != "generated" || record.Origin != "provider:test" || record.RunID != "run-output" {
		t.Fatalf("generated record = %#v", record)
	}
	_, reader, err := service.Open(context.Background(), mgr.GetHeader().ID, record.ID)
	if err != nil {
		t.Fatalf("open generated artifact: %v", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "generated report" {
		t.Fatalf("artifact content = %q, %v", data, err)
	}
	_ = commondb.CloseAll()
}

func TestAttachmentDeliveryTransitionsFromPending(t *testing.T) {
	root := t.TempDir()
	mgr := session.New(t.TempDir(), root)
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	service, err := NewAttachmentService(root, DefaultAttachmentPolicy())
	if err != nil {
		t.Fatalf("new attachment service: %v", err)
	}
	attachment, err := service.Accept(context.Background(), mgr.GetHeader().ID, "run-1", AttachmentIngress{
		Origin: "test", Kind: AttachmentFile, Filename: "result.txt", MediaType: "text/plain",
		Open: func(context.Context) (AttachmentStream, error) {
			return AttachmentStream{Reader: io.NopCloser(strings.NewReader("result"))}, nil
		},
	})
	if err != nil {
		t.Fatalf("accept attachment: %v", err)
	}
	delivery, err := service.BeginDelivery(context.Background(), attachment, "feishu", "oc_test")
	if err != nil {
		t.Fatalf("begin delivery: %v", err)
	}
	if delivery.Status != "pending" || delivery.ID == "" {
		t.Fatalf("delivery = %#v", delivery)
	}
	if err := service.FinishDelivery(context.Background(), delivery.ID, "delivered", "om_sent", ""); err != nil {
		t.Fatalf("finish delivery: %v", err)
	}
	if err := service.FinishDelivery(context.Background(), delivery.ID, "delivered", "om_duplicate", ""); err == nil {
		t.Fatal("terminal delivery transitioned twice")
	}
	_ = commondb.CloseAll()
}

func TestAttachmentServiceCleanupExpiresPrivateContent(t *testing.T) {
	root := t.TempDir()
	mgr := session.New(t.TempDir(), root)
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	service, err := NewAttachmentService(root, AttachmentPolicy{
		MaxImageBytes: 1 << 20,
		MaxFileBytes:  1 << 20,
		Retention:     time.Hour,
	})
	if err != nil {
		t.Fatalf("new attachment service: %v", err)
	}
	record, err := service.Accept(context.Background(), mgr.GetHeader().ID, "run-1", AttachmentIngress{
		Origin: "test", Kind: AttachmentFile, Filename: "expired.txt",
		Open: func(context.Context) (AttachmentStream, error) {
			return AttachmentStream{Reader: io.NopCloser(strings.NewReader("expired"))}, nil
		},
	})
	if err != nil {
		t.Fatalf("accept attachment: %v", err)
	}
	if err := session.WriteRootDatabase(context.Background(), root, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE session_attachments SET expires_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), record.ID)
		return err
	}); err != nil {
		t.Fatalf("expire fixture: %v", err)
	}
	count, err := service.CleanupExpired(context.Background())
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if count != 1 {
		t.Fatalf("expired attachment count = %d, want 1", count)
	}
	stored, err := service.Get(context.Background(), mgr.GetHeader().ID, record.ID)
	if err != nil || stored.Status != "expired" {
		t.Fatalf("expired record = %#v, %v", stored, err)
	}
	if _, _, err := service.Open(context.Background(), mgr.GetHeader().ID, record.ID); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("Open expired attachment error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(record.StorageKey))); !os.IsNotExist(err) {
		t.Fatalf("expired content still exists or stat failed: %v", err)
	}
	_ = commondb.CloseAll()
}

func TestPublishArtifactCopiesWorkDirectoryFileIntoRuntimeStorage(t *testing.T) {
	root := t.TempDir()
	workDir := t.TempDir()
	mgr := session.New(workDir, root)
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "report.txt"), []byte("first version"), 0600); err != nil {
		t.Fatalf("write artifact fixture: %v", err)
	}
	service, err := NewAttachmentService(root, DefaultAttachmentPolicy())
	if err != nil {
		t.Fatalf("new attachment service: %v", err)
	}
	runtime := &SessionRuntime{
		ID: mgr.GetHeader().ID, WorkDir: workDir, Attachments: service,
		Registry: tools.NewRegistry(workDir, nil),
	}
	collector, err := runtime.BeginArtifactCollection("run-output")
	if err != nil {
		t.Fatalf("BeginArtifactCollection: %v", err)
	}
	tool, ok := runtime.Registry.Get("publish_artifact")
	if !ok {
		t.Fatal("publish_artifact tool was not registered")
	}
	result, err := tool.Execute(context.Background(), map[string]any{"path": "report.txt"})
	if err != nil {
		t.Fatalf("publish_artifact: %v", err)
	}
	if !strings.Contains(result.Text, "Published generated file") {
		t.Fatalf("publish result = %#v", result)
	}
	items := collector.Artifacts()
	if len(items) != 1 || items[0].Status != "generated" || items[0].RunID != "run-output" {
		t.Fatalf("collected artifacts = %#v", items)
	}
	// Publishing must copy the final bytes, not retain a mutable worktree path.
	if err := os.WriteFile(filepath.Join(workDir, "report.txt"), []byte("mutated"), 0600); err != nil {
		t.Fatalf("mutate source artifact: %v", err)
	}
	_, reader, err := service.Open(context.Background(), mgr.GetHeader().ID, items[0].ID)
	if err != nil {
		t.Fatalf("open published artifact: %v", err)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || string(data) != "first version" {
		t.Fatalf("published content = %q, read=%v close=%v", data, readErr, closeErr)
	}
	collector.Close()
	if _, ok := runtime.Registry.Get("publish_artifact"); ok {
		t.Fatal("publish_artifact tool remained registered after collection close")
	}
	_ = commondb.CloseAll()
}

func TestProjectDeliveriesUsesCapabilitiesForNativeAndFallback(t *testing.T) {
	root := t.TempDir()
	mgr := session.New(t.TempDir(), root)
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	service, err := NewAttachmentService(root, DefaultAttachmentPolicy())
	if err != nil {
		t.Fatalf("new attachment service: %v", err)
	}
	attachment, err := service.Accept(context.Background(), mgr.GetHeader().ID, "run-1", AttachmentIngress{
		Origin: "test", Kind: AttachmentFile, Filename: "result.txt", MediaType: "text/plain",
		Open: func(context.Context) (AttachmentStream, error) {
			return AttachmentStream{Reader: io.NopCloser(strings.NewReader("result"))}, nil
		},
	})
	if err != nil {
		t.Fatalf("accept attachment: %v", err)
	}
	unsupported, err := service.ProjectDeliveries(context.Background(), []SessionAttachment{attachment}, "wechat", "wx-user", DeliveryCapability{Text: true})
	if err != nil {
		t.Fatalf("project fallback: %v", err)
	}
	if len(unsupported.Operations) != 0 || !strings.Contains(unsupported.FallbackText, "result.txt") {
		t.Fatalf("fallback projection = %#v", unsupported)
	}
	native, err := service.ProjectDeliveries(context.Background(), []SessionAttachment{attachment}, "feishu", "oc-chat", DeliveryCapability{Text: true, SendFile: true})
	if err != nil {
		t.Fatalf("project native: %v", err)
	}
	if len(native.Operations) != 1 || native.Operations[0].Delivery.Status != "pending" || native.FallbackText != "" {
		t.Fatalf("native projection = %#v", native)
	}
	if err := service.FinishDelivery(context.Background(), native.Operations[0].Delivery.ID, "delivered", "om-output", ""); err != nil {
		t.Fatalf("finish native delivery: %v", err)
	}
	_ = commondb.CloseAll()
}

type attachmentResolverProvider struct{}

func (attachmentResolverProvider) Name() string                    { return "test" }
func (attachmentResolverProvider) API() string                     { return "test" }
func (attachmentResolverProvider) Models() []*provider.Model       { return nil }
func (attachmentResolverProvider) GetModel(string) *provider.Model { return nil }
func (attachmentResolverProvider) Chat(context.Context, provider.ChatParams) <-chan provider.StreamEvent {
	ch := make(chan provider.StreamEvent)
	close(ch)
	return ch
}
func (attachmentResolverProvider) ResolveAttachment(context.Context, string) (provider.AttachmentContent, error) {
	return provider.AttachmentContent{Data: []byte("generated report"), Filename: "report.txt", MediaType: "text/plain"}, nil
}

func TestAttachmentServiceRejectsOversizedInputWithoutPersisting(t *testing.T) {
	root := t.TempDir()
	mgr := session.New(t.TempDir(), root)
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	service, err := NewAttachmentService(root, AttachmentPolicy{
		MaxImageBytes: 4,
		MaxFileBytes:  4,
		Retention:     time.Hour,
	})
	if err != nil {
		t.Fatalf("new attachment service: %v", err)
	}
	_, err = service.Accept(context.Background(), mgr.GetHeader().ID, "run-1", AttachmentIngress{
		Origin: "test", Kind: AttachmentFile, Filename: "large.bin",
		Open: func(context.Context) (AttachmentStream, error) {
			return AttachmentStream{Reader: io.NopCloser(strings.NewReader("12345"))}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds 4 bytes") {
		t.Fatalf("oversized error = %v", err)
	}
	var count int
	if err := session.QueryRootDatabase(root, func(db *sql.DB) error {
		return db.QueryRow(`SELECT COUNT(*) FROM session_attachments WHERE session_id = ?`, mgr.GetHeader().ID).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("persisted attachment count = %d, want 0", count)
	}
	_ = commondb.CloseAll()
}
