package openaiapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebUILoginUsesConfiguredTokenAsPassword(t *testing.T) {
	cfg := AuthConfig{Enabled: true, Tokens: []string{"auth-token"}}
	login := WebUILoginHandler(cfg)

	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"auth-token"}`))
	loginResp := httptest.NewRecorder()
	login.ServeHTTP(loginResp, loginReq)
	if loginResp.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", loginResp.Code, loginResp.Body.String())
	}
	cookies := loginResp.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != webUISessionCookieName || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge <= 0 {
		t.Fatalf("unexpected login cookie: %#v", cookie)
	}

	protected := AuthMiddleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	protectedReq := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	protectedReq.AddCookie(cookie)
	protectedResp := httptest.NewRecorder()
	protected.ServeHTTP(protectedResp, protectedReq)
	if protectedResp.Code != http.StatusNoContent {
		t.Fatalf("cookie-authenticated request status = %d, want %d", protectedResp.Code, http.StatusNoContent)
	}
}

func TestWebUILoginRejectsInvalidPassword(t *testing.T) {
	handler := WebUILoginHandler(AuthConfig{Enabled: true, Tokens: []string{"auth-token"}})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"wrong"}`))
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("login status = %d, want %d", resp.Code, http.StatusUnauthorized)
	}
	if len(resp.Result().Cookies()) != 0 {
		t.Fatal("invalid login unexpectedly set a cookie")
	}
}

func TestAuthMiddlewareAllowsOnlyWebUIBootstrapAssetsWithoutCredentials(t *testing.T) {
	handler := AuthMiddleware(AuthConfig{Enabled: true, Tokens: []string{"auth-token"}}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, path := range []string{"/", "/index.html", "/assets/app.js", "/mothx-small.ico"} {
		t.Run(path, func(t *testing.T) {
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, path, nil))
			if resp.Code != http.StatusNoContent {
				t.Fatalf("%s status = %d, want %d", path, resp.Code, http.StatusNoContent)
			}
		})
	}

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("protected API status = %d, want %d", resp.Code, http.StatusUnauthorized)
	}
}

func TestWebUIAuthStatusDoesNotExposeTokens(t *testing.T) {
	handler := WebUIAuthStatusHandler(AuthConfig{Enabled: true, Tokens: []string{"auth-token"}})
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status endpoint = %d, want %d", resp.Code, http.StatusOK)
	}
	if strings.Contains(resp.Body.String(), "auth-token") {
		t.Fatalf("status endpoint exposed auth token: %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"authenticated":false`) {
		t.Fatalf("unexpected status response: %s", resp.Body.String())
	}
}

func TestAuthMiddlewareForConfigUsesLatestConfig(t *testing.T) {
	cfg := AuthConfig{Enabled: false}
	handler := AuthMiddlewareForConfig(func() AuthConfig { return cfg }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if first.Code != http.StatusNoContent {
		t.Fatalf("disabled auth status = %d, want %d", first.Code, http.StatusNoContent)
	}

	cfg = AuthConfig{Enabled: true, Tokens: []string{"updated-token"}}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("updated auth status = %d, want %d", second.Code, http.StatusUnauthorized)
	}

	thirdReq := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	thirdReq.Header.Set("Authorization", "Bearer updated-token")
	third := httptest.NewRecorder()
	handler.ServeHTTP(third, thirdReq)
	if third.Code != http.StatusNoContent {
		t.Fatalf("updated token status = %d, want %d", third.Code, http.StatusNoContent)
	}
}
