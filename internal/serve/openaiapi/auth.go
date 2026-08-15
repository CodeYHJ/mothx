package openaiapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const webUISessionCookieName = "mothx_webui_auth"

// AuthMiddleware returns an HTTP middleware that validates Bearer tokens or a
// Web UI session cookie. If auth is disabled, the handler is called directly.
func AuthMiddleware(cfg AuthConfig, next http.Handler) http.Handler {
	return AuthMiddlewareForConfig(func() AuthConfig { return cfg }, next)
}

// AuthMiddlewareForConfig validates each request against the current auth
// configuration. It lets a running Serve instance apply auth changes without
// rebuilding its HTTP handler tree.
func AuthMiddlewareForConfig(getConfig func() AuthConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := getAuthConfig(getConfig)
		if !cfg.Enabled {
			next.ServeHTTP(w, r)
			return
		}
		if isPublicWebUIAssetRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !hasConfiguredTokens(cfg) {
			writeError(w, http.StatusUnauthorized, "authentication is enabled but no API tokens are configured", "authentication_error")
			return
		}
		if !isAuthorizedRequest(r, cfg) {
			writeError(w, http.StatusUnauthorized, "missing or invalid authentication credentials", "authentication_error")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// WebUILoginHandler accepts one configured API auth token as the Web UI
// password and creates an HttpOnly signed browser session cookie.
func WebUILoginHandler(cfg AuthConfig) http.Handler {
	return WebUILoginHandlerForConfig(func() AuthConfig { return cfg })
}

// WebUILoginHandlerForConfig accepts a configured token using the current
// runtime auth configuration.
func WebUILoginHandlerForConfig(getConfig func() AuthConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		cfg := getAuthConfig(getConfig)
		if !cfg.Enabled {
			writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true, "authEnabled": false})
			return
		}

		var body struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil || !validToken(cfg.Tokens, body.Password) {
			writeError(w, http.StatusUnauthorized, "invalid password", "authentication_error")
			return
		}

		sessionValue, err := newWebUISessionValue(body.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "create login session", "server_error")
			return
		}
		expires := time.Now().Add(7 * 24 * time.Hour)
		http.SetCookie(w, &http.Cookie{
			Name:     webUISessionCookieName,
			Value:    sessionValue,
			Path:     "/",
			Expires:  expires,
			MaxAge:   int(time.Until(expires).Seconds()),
			HttpOnly: true,
			Secure:   requestIsHTTPS(r),
			SameSite: http.SameSiteStrictMode,
		})
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true, "authEnabled": true})
	})
}

// WebUIAuthStatusHandler reports whether the current browser request has an
// authenticated Web UI session. It never exposes configured tokens.
func WebUIAuthStatusHandler(cfg AuthConfig) http.Handler {
	return WebUIAuthStatusHandlerForConfig(func() AuthConfig { return cfg })
}

// WebUIAuthStatusHandlerForConfig reports authentication state from the
// current runtime auth configuration.
func WebUIAuthStatusHandlerForConfig(getConfig func() AuthConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		cfg := getAuthConfig(getConfig)
		writeJSON(w, http.StatusOK, map[string]bool{
			"authenticated": !cfg.Enabled || (hasConfiguredTokens(cfg) && isAuthorizedRequest(r, cfg)),
			"authEnabled":   cfg.Enabled,
		})
	})
}

func getAuthConfig(getConfig func() AuthConfig) AuthConfig {
	if getConfig == nil {
		return AuthConfig{}
	}
	return getConfig()
}

// WebUILogoutHandler clears the Web UI session cookie. It is intentionally
// callable without an existing session so a stale or invalid cookie can be removed.
func WebUILogoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     webUISessionCookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   requestIsHTTPS(r),
			SameSite: http.SameSiteStrictMode,
		})
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
	})
}

func hasConfiguredTokens(cfg AuthConfig) bool {
	return len(cfg.Tokens) > 0
}

func isAuthorizedRequest(r *http.Request, cfg AuthConfig) bool {
	return validToken(cfg.Tokens, extractBearerToken(r)) || validWebUISession(cfg.Tokens, r)
}

func validToken(tokens []string, candidate string) bool {
	if candidate == "" {
		return false
	}
	matched := 0
	for _, token := range tokens {
		if len(token) == len(candidate) {
			matched |= subtle.ConstantTimeCompare([]byte(token), []byte(candidate))
		}
	}
	return matched == 1
}

func newWebUISessionValue(token string) (string, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(nonce) + "." + base64.RawURLEncoding.EncodeToString(signWebUISession(token, nonce)), nil
}

func validWebUISession(tokens []string, r *http.Request) bool {
	cookie, err := r.Cookie(webUISessionCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(nonce) != 32 {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size {
		return false
	}

	matched := 0
	for _, token := range tokens {
		expected := signWebUISession(token, nonce)
		matched |= subtle.ConstantTimeCompare(expected, signature)
	}
	return matched == 1
}

func signWebUISession(token string, nonce []byte) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write(nonce)
	return mac.Sum(nil)
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func isPublicWebUIAssetRequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	switch r.URL.Path {
	case "/", "/index.html", "/mothx-small.ico":
		return true
	default:
		return strings.HasPrefix(r.URL.Path, "/assets/")
	}
}

// CORSMiddleware adds CORS headers when enabled.
func CORSMiddleware(cfg CORSConfig, next http.Handler) http.Handler {
	if !cfg.Enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := allowedCORSOrigin(cfg, r.Header.Get("Origin")); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedCORSOrigin(cfg CORSConfig, requestOrigin string) string {
	if len(cfg.AllowOrigins) == 0 {
		return "*"
	}
	for _, allowed := range cfg.AllowOrigins {
		if allowed == "*" {
			return "*"
		}
		if requestOrigin != "" && allowed == requestOrigin {
			return requestOrigin
		}
	}
	if requestOrigin == "" && len(cfg.AllowOrigins) == 1 {
		return cfg.AllowOrigins[0]
	}
	return ""
}

// ConcurrencyMiddleware limits the number of concurrent in-flight requests.
// If maxConcurrent <= 0, no limit is applied.
func ConcurrencyMiddleware(maxConcurrent int, next http.Handler) http.Handler {
	if maxConcurrent <= 0 {
		return next
	}
	sem := make(chan struct{}, maxConcurrent)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		default:
			writeError(w, http.StatusTooManyRequests, "server is at capacity, please retry later", "rate_limit_error")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return ""
	}
	return strings.TrimSpace(auth[len(prefix):])
}
