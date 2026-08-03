package openaiapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesResponsesRunAPI(t *testing.T) {
	srv := newTestServer(t)
	mux := http.NewServeMux()
	registerRoutes(mux, srv, RunOptions{})

	req := httptest.NewRequest(http.MethodGet, "/api/responses/runs/run-1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("Responses run route returned 404; route was not registered")
	}
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("Responses run route status = %d, want 501 for non-Responses provider", w.Code)
	}
}

func TestRegisterRoutesResponsesRunAPIDisabledWithAPI(t *testing.T) {
	srv := newTestServer(t)
	mux := http.NewServeMux()
	registerRoutes(mux, srv, RunOptions{DisableAPI: true})

	req := httptest.NewRequest(http.MethodGet, "/api/responses/runs/run-1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled Responses run route status = %d, want 404", w.Code)
	}
}
