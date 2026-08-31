package openaiapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFirstMessageFreshSession repro: the very first WebUI message submitted
// against a brand-new client-created session must be accepted (202), not
// rejected with 409 "session already has an active run".
func TestFirstMessageFreshSession(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()

	sessionID := "webui-" + strings.ReplaceAll(newCompletionID(), "_", "x")
	body := `{"message":"first message","transcript":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/runs", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "repro-one")
	w := httptest.NewRecorder()
	srv.HandleSubmitRun(w, req)

	t.Logf("first submit status = %d, body = %s", w.Code, w.Body.String())
	if w.Code != http.StatusAccepted {
		var resp struct {
			Error json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		t.Fatalf("first message status = %d, want 202; error = %s", w.Code, string(resp.Error))
	}
}
