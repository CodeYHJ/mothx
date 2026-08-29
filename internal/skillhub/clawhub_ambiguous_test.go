package skillhub

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const ambiguousCustomMailPayload = `{"code":"AMBIGUOUS_SKILL_SLUG","message":"Found multiple skills with the slug \"custom-mail-fresh100\"; specify which one you want to install:","slug":"custom-mail-fresh100","matches":[{"ownerHandle":"xuxuclassmate","slug":"custom-mail-fresh100","ref":"@xuxuclassmate/custom-mail-fresh100","url":"https://clawhub.ai/xuxuclassmate/skills/custom-mail-fresh100"},{"ownerHandle":"xuxuclassmate","slug":"custom-mail","ref":"@xuxuclassmate/custom-mail","url":"https://clawhub.ai/xuxuclassmate/skills/custom-mail"}]}`

func TestClawHubDetailResolvesAmbiguousSlug(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.RawQuery)
		if r.URL.Query().Get("owner") == "" {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, ambiguousCustomMailPayload)
			return
		}
		_, _ = io.WriteString(w, `{"skill":{"slug":"custom-mail-fresh100","displayName":"Fresh Mail","summary":"mail","version":"1.2.0"}}`)
	}))
	defer server.Close()
	detail, err := NewClawHubClient(server.URL, nil).Detail(context.Background(), SkillID{Market: MarketClawHub, ID: "custom-mail-fresh100"})
	if err != nil {
		t.Fatal(err)
	}
	if detail.ID != "custom-mail-fresh100" || detail.Version != "1.2.0" || detail.Name != "Fresh Mail" {
		t.Fatalf("unexpected detail: %#v", detail)
	}
	if len(calls) != 2 || calls[1] != "owner=xuxuclassmate" {
		t.Fatalf("expected owner retry, got calls: %v", calls)
	}
}

func TestClawHubDetailCachesResolvedOwner(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("owner") == "" {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, ambiguousCustomMailPayload)
			return
		}
		if r.URL.Query().Get("owner") != "xuxuclassmate" {
			t.Errorf("owner = %q", r.URL.Query().Get("owner"))
		}
		_, _ = io.WriteString(w, `{"skill":{"slug":"custom-mail-fresh100","version":"1.2.0"}}`)
	}))
	defer server.Close()
	client := NewClawHubClient(server.URL, nil)
	if _, err := client.Detail(context.Background(), SkillID{Market: MarketClawHub, ID: "custom-mail-fresh100"}); err != nil {
		t.Fatal(err)
	}
	// The resolved owner is cached, so Files and DownloadSources reuse it.
	files, err := client.Files(context.Background(), SkillID{Market: MarketClawHub, ID: "custom-mail-fresh100"}, "1.2.0")
	if err != nil || len(files) != 0 {
		t.Fatalf("files = %#v, %v", files, err)
	}
	sources := client.DownloadSources(SkillID{Market: MarketClawHub, ID: "custom-mail-fresh100"}, "1.2.0")
	if len(sources) != 1 || !strings.Contains(sources[0].URL, "owner=xuxuclassmate") {
		t.Fatalf("download sources = %#v", sources)
	}
}

func TestClawHubAmbiguousSlugWithoutExactMatchReturnsHelpfulError(t *testing.T) {
	payload := `{"code":"AMBIGUOUS_SKILL_SLUG","message":"Found multiple skills with the slug \"custom-mail-fresh100\"; specify which one you want to install:","slug":"custom-mail-fresh100","matches":[{"ownerHandle":"alice","slug":"other-mail","ref":"@alice/other-mail","url":"https://clawhub.ai/alice/skills/other-mail"},{"ownerHandle":"bob","slug":"something-else","ref":"@bob/something-else","url":"https://clawhub.ai/bob/skills/something-else"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()
	_, err := NewClawHubClient(server.URL, nil).Detail(context.Background(), SkillID{Market: MarketClawHub, ID: "custom-mail-fresh100"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "@alice/other-mail") || !strings.Contains(err.Error(), "@bob/something-else") {
		t.Fatalf("error should list candidate refs, got: %v", err)
	}
	if !strings.Contains(err.Error(), "custom-mail-fresh100") {
		t.Fatalf("error should mention the requested slug, got: %v", err)
	}
}

func TestClawHubInstallResolvesAmbiguousSlugEndToEnd(t *testing.T) {
	archive := makeArchive(t, map[string]string{"SKILL.md": "# Mail\n"})
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case strings.HasSuffix(r.URL.Path, "/download"):
			if r.URL.Query().Get("owner") == "" {
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, ambiguousCustomMailPayload)
				return
			}
			_, _ = w.Write(archive)
		default:
			if r.URL.Query().Get("owner") == "" {
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, ambiguousCustomMailPayload)
				return
			}
			_, _ = io.WriteString(w, `{"skill":{"slug":"custom-mail-fresh100","version":"1.2.0"}}`)
		}
	}))
	defer server.Close()
	result, err := Install(context.Background(), NewClawHubClient(server.URL, nil), InstallRequest{Market: MarketClawHub, ID: "custom-mail-fresh100", Scope: "project", TargetDir: t.TempDir()})
	if err != nil {
		t.Fatalf("install failed: %v (calls: %v)", err, calls)
	}
	if !result.Installed || result.Name != "custom-mail-fresh100" {
		t.Fatalf("unexpected result: %#v", result)
	}
	seen := false
	for _, call := range calls {
		if strings.HasSuffix(call, "/download?version=1.2.0&owner=xuxuclassmate") || strings.HasSuffix(call, "/download?owner=xuxuclassmate&version=1.2.0") {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("owner-qualified download not attempted: %v", calls)
	}
}

func TestClawHubExplicitOwnerRefBypassesAmbiguity(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.RawQuery)
		if r.URL.Query().Get("owner") == "" {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, ambiguousCustomMailPayload)
			return
		}
		_, _ = io.WriteString(w, `{"skill":{"slug":"custom-mail-fresh100","version":"1.2.0"}}`)
	}))
	defer server.Close()
	client := NewClawHubClient(server.URL, nil)
	if _, err := client.Detail(context.Background(), SkillID{Market: MarketClawHub, ID: "@xuxuclassmate/custom-mail-fresh100"}); err != nil {
		t.Fatal(err)
	}
	// Explicit owner refs never hit the ambiguity path: exactly one request.
	if len(calls) != 1 || calls[0] != "owner=xuxuclassmate" {
		t.Fatalf("calls = %v", calls)
	}
}
