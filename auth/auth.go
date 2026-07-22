package auth

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	tokenLength       = 32
	sessionTTL        = time.Hour
	preloadedTokenTTL = 24 * time.Hour
)

// tokenAlphabet has 64 URL-safe characters so a random byte maps to a character
// with no modulo bias (256 % 64 == 0).
const tokenAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// AuthManager manages user authentication and token storage. Its pure core
// methods (CreateSession, EndSession, Authenticate) are transport-agnostic; the
// Login, Logout, and HandlePreflight methods are thin HTTP adapters over them.
type AuthManager struct {
	existingTokens map[string]string    // token -> username (preloaded)
	loggedInToken  map[string]string    // token -> username (session)
	expiry         map[string]time.Time // token -> expiration time
	mtx            sync.Mutex
}

// NewAuthManager creates an AuthManager seeded with preloaded token->username
// mappings (typically loaded from a tokens file).
func NewAuthManager(existingTokens map[string]string) *AuthManager {
	return &AuthManager{
		existingTokens: existingTokens,
		loggedInToken:  make(map[string]string),
		expiry:         make(map[string]time.Time),
	}
}

// SetExistingTokens sets expiration for preloaded tokens.
func (am *AuthManager) SetExistingTokens() {
	for token := range am.existingTokens {
		am.expiry[token] = time.Now().Add(preloadedTokenTTL)
	}
}

// GenerateToken returns a cryptographically random access token.
func GenerateToken() string {
	raw := make([]byte, tokenLength)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand failure is unrecoverable and must never yield a weak token.
		panic(fmt.Sprintf("auth: reading random bytes: %v", err))
	}
	token := make([]byte, tokenLength)
	for i, b := range raw {
		token[i] = tokenAlphabet[b&63]
	}
	return string(token)
}

// CreateSession issues a new session token for a user. It is transport-agnostic.
func (am *AuthManager) CreateSession(username string) (token string, expiresIn int) {
	token = GenerateToken()
	am.mtx.Lock()
	am.loggedInToken[token] = username
	am.expiry[token] = time.Now().Add(sessionTTL)
	am.mtx.Unlock()
	return token, int(sessionTTL.Seconds())
}

// EndSession invalidates a token. It returns true when the token was a known
// session or preloaded token, and false when it was unknown.
func (am *AuthManager) EndSession(token string) bool {
	am.mtx.Lock()
	defer am.mtx.Unlock()
	if _, ok := am.loggedInToken[token]; ok {
		delete(am.loggedInToken, token)
		delete(am.expiry, token)
		return true
	}
	// Preloaded tokens remain valid for their TTL; treat logout as a no-op success.
	_, ok := am.existingTokens[token]
	return ok
}

// Authenticate resolves a token to a username, enforcing expiry. It is the pure
// core used by both the HTTP middleware and Login/Logout adapters.
func (am *AuthManager) Authenticate(token string) (string, bool) {
	am.mtx.Lock()
	defer am.mtx.Unlock()

	username, isSession := am.loggedInToken[token]
	if !isSession {
		if u, isPreloaded := am.existingTokens[token]; isPreloaded {
			username = u
		} else {
			return "", false
		}
	}

	if time.Now().After(am.expiry[token]) {
		delete(am.loggedInToken, token)
		delete(am.expiry, token)
		return "", false
	}
	return username, true
}

func writeAuthError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

// HandlePreflight handles CORS preflight requests.
func (am *AuthManager) HandlePreflight(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.WriteHeader(http.StatusOK)
}

// Login creates a new session and returns an access token.
func (am *AuthManager) Login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	var req struct {
		Username string `json:"username"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Username) == "" {
		writeAuthError(w, http.StatusBadRequest, "invalid_request", "username is required")
		return
	}

	token, expiresIn := am.CreateSession(req.Username)

	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   expiresIn,
	})
}

// Logout invalidates a session token.
func (am *AuthManager) Logout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	token, ok := bearerToken(r)
	if !ok {
		writeAuthError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid authorization header")
		return
	}

	if am.EndSession(token) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeAuthError(w, http.StatusUnauthorized, "unauthorized", "unknown token")
}

// ValidateToken is the transport-facing name for Authenticate.
func (am *AuthManager) ValidateToken(token string) (string, bool) {
	return am.Authenticate(token)
}

// bearerToken extracts a bearer token from the Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	token := r.Header.Get("Authorization")
	if len(token) > 7 && strings.HasPrefix(token, "Bearer ") {
		return strings.TrimPrefix(token, "Bearer "), true
	}
	return "", false
}

// GetUsernameFromRequest extracts the username from the request's bearer token.
func (am *AuthManager) GetUsernameFromRequest(r *http.Request) (string, error) {
	token, ok := bearerToken(r)
	if !ok {
		return "", fmt.Errorf("no token provided or invalid token format")
	}
	username, valid := am.Authenticate(token)
	if !valid {
		return "", fmt.Errorf("invalid or expired token")
	}
	return username, nil
}
