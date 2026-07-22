package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGenerateToken(t *testing.T) {
	token := GenerateToken()
	if len(token) != tokenLength {
		t.Errorf("Expected token of length %d, got %d", tokenLength, len(token))
	}
}

func TestLoginNewUser(t *testing.T) {
	am := NewAuthManager(map[string]string{})

	req := httptest.NewRequest("POST", "/api/session", strings.NewReader(`{"username":"alice"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	am.Login(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var response map[string]any
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	token, ok := response["access_token"].(string)
	if !ok || len(token) != tokenLength {
		t.Fatalf("Expected access_token of length %d", tokenLength)
	}
	if response["token_type"] != "Bearer" {
		t.Errorf("Expected token_type Bearer")
	}
}

func TestLogout(t *testing.T) {
	am := NewAuthManager(map[string]string{})
	am.loggedInToken["validtoken"] = "testuser"
	am.expiry["validtoken"] = time.Now().Add(time.Hour)

	req := httptest.NewRequest("DELETE", "/api/session", nil)
	req.Header.Set("Authorization", "Bearer validtoken")
	w := httptest.NewRecorder()
	am.Logout(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("Expected 204, got %d", w.Code)
	}
	if _, valid := am.ValidateToken("validtoken"); valid {
		t.Errorf("Expected token to be invalidated")
	}
}

func TestValidateOldToken(t *testing.T) {
	am := NewAuthManager(map[string]string{
		"oldvalidtoken": "olduser",
	})
	am.loggedInToken["newtoken"] = "newuser"
	am.expiry["oldvalidtoken"] = time.Now().Add(24 * time.Hour)

	username, valid := am.ValidateToken("oldvalidtoken")
	if !valid {
		t.Fatalf("Expected token to be valid")
	}
	if username != "olduser" {
		t.Errorf("Expected olduser, got %s", username)
	}

	am.expiry["oldvalidtoken"] = time.Now().Add(-time.Hour)
	if _, valid = am.ValidateToken("oldvalidtoken"); valid {
		t.Errorf("Expected token to be expired")
	}
}

func TestValidateNewToken(t *testing.T) {
	am := NewAuthManager(map[string]string{
		"oldvalidtoken": "olduser",
	})
	am.loggedInToken["newtoken"] = "newuser"
	am.expiry["newtoken"] = time.Now().Add(time.Hour)

	username, valid := am.ValidateToken("newtoken")
	if !valid {
		t.Fatalf("Expected token to be valid")
	}
	if username != "newuser" {
		t.Errorf("Expected newuser, got %s", username)
	}

	am.expiry["newtoken"] = time.Now().Add(-time.Hour)
	if _, valid = am.ValidateToken("newtoken"); valid {
		t.Errorf("Expected token to be expired")
	}
}
