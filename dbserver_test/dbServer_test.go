package dbServer_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/muradImre/database-design/btreeidx"
	"github.com/muradImre/database-design/db"
	"github.com/muradImre/database-design/dbServer"
	"github.com/muradImre/database-design/patch"
	"github.com/muradImre/database-design/schema/parser"
	"github.com/muradImre/database-design/schema/validator"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

type SchemaValidatorFactory struct {
	schema *jsonschema.Schema
}

func (f *SchemaValidatorFactory) NewValidator() dbServer.Validator {
	return validator.New(f.schema)
}

type AuthManagerFactory struct {
	am *MockAuthManager
}

func (f *AuthManagerFactory) NewAuthManager() dbServer.AuthService { return f.am }

type HrefResponseBody struct {
	Href string `json:"href"`
}

// MockAuthManager satisfies dbServer.AuthService and treats every request as an
// authenticated user "user1".
type MockAuthManager struct{}

func (m *MockAuthManager) Login(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
func (m *MockAuthManager) Logout(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
func (m *MockAuthManager) ValidateToken(token string) (string, bool) { return "user1", true }
func (m *MockAuthManager) HandlePreflight(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
func (m *MockAuthManager) GetUsernameFromRequest(r *http.Request) (string, error) {
	return "user1", nil
}

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	authFactory := &AuthManagerFactory{am: &MockAuthManager{}}
	dbFactory := func(name string) dbServer.DB {
		return db.New(name, func() db.DBIndex[string, db.Document] {
			return btreeidx.New[string, db.Document]()
		})
	}
	databasesFactory := func() dbServer.DBIndex[string, dbServer.DB] {
		return btreeidx.New[string, dbServer.DB]()
	}
	sch, err := parser.SchemaParser("../schemas/document.json")
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	validatorFactory := &SchemaValidatorFactory{schema: sch}
	return dbServer.New(12345, authFactory, dbFactory, databasesFactory, validatorFactory, patch.New()).Handler
}

// do issues a request and returns the response recorder.
func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestCreateStore(t *testing.T) {
	h := newHandler(t)

	w := do(t, h, "PUT", "/api/stores/db1", "")
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	var body HrefResponseBody
	json.Unmarshal(w.Body.Bytes(), &body)
	if body.Href != "/api/stores/db1" {
		t.Fatalf("unexpected href %q", body.Href)
	}
	if loc := w.Result().Header.Get("Location"); loc != body.Href {
		t.Fatalf("location %q != href %q", loc, body.Href)
	}

	// Duplicate store -> 400.
	if w := do(t, h, "PUT", "/api/stores/db1", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate store expected 400, got %d", w.Code)
	}
	// Second unique store -> 201.
	if w := do(t, h, "PUT", "/api/stores/db2", ""); w.Code != http.StatusCreated {
		t.Fatalf("second store expected 201, got %d", w.Code)
	}
}

func TestDocumentLifecycle(t *testing.T) {
	h := newHandler(t)
	do(t, h, "PUT", "/api/stores/db1", "")

	// Create a document.
	w := do(t, h, "PUT", "/api/stores/db1/docs/p1", `{"name":"Ada","age":36}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create doc expected 201, got %d", w.Code)
	}
	var body HrefResponseBody
	json.Unmarshal(w.Body.Bytes(), &body)
	if body.Href != "/api/stores/db1/docs/p1" {
		t.Fatalf("unexpected href %q", body.Href)
	}

	// Read it back.
	w = do(t, h, "GET", "/api/stores/db1/docs/p1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("get doc expected 200, got %d", w.Code)
	}
	var view dbServer.DocumentView
	json.Unmarshal(w.Body.Bytes(), &view)
	if view.ID != "p1" {
		t.Fatalf("expected id p1, got %q", view.ID)
	}
	if view.Metadata.CreatedBy != "user1" {
		t.Fatalf("expected created_by user1, got %q", view.Metadata.CreatedBy)
	}

	// Replace it (existing -> 200).
	if w := do(t, h, "PUT", "/api/stores/db1/docs/p1", `{"name":"Ada","age":37}`); w.Code != http.StatusOK {
		t.Fatalf("replace doc expected 200, got %d", w.Code)
	}

	// Delete it.
	if w := do(t, h, "DELETE", "/api/stores/db1/docs/p1", ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete expected 204, got %d", w.Code)
	}
	if w := do(t, h, "GET", "/api/stores/db1/docs/p1", ""); w.Code != http.StatusNotFound {
		t.Fatalf("get deleted expected 404, got %d", w.Code)
	}
}

func TestIfExistsFail(t *testing.T) {
	h := newHandler(t)
	do(t, h, "PUT", "/api/stores/db1", "")
	do(t, h, "PUT", "/api/stores/db1/docs/p1", `{"name":"Ada","age":36}`)

	w := do(t, h, "PUT", "/api/stores/db1/docs/p1?if_exists=fail", `{"name":"Bob","age":1}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("if_exists=fail on existing expected 409, got %d", w.Code)
	}
}

func TestSchemaValidation(t *testing.T) {
	h := newHandler(t)
	do(t, h, "PUT", "/api/stores/db1", "")
	// Missing required "age".
	if w := do(t, h, "PUT", "/api/stores/db1/docs/bad", `{"name":"NoAge"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid schema expected 400, got %d", w.Code)
	}
}

func TestListAndRange(t *testing.T) {
	h := newHandler(t)
	do(t, h, "PUT", "/api/stores/db1", "")
	for _, id := range []string{"c", "a", "e", "b", "d"} {
		do(t, h, "PUT", "/api/stores/db1/docs/"+id, `{"name":"x","age":1}`)
	}

	w := do(t, h, "GET", "/api/stores/db1/docs", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list expected 200, got %d", w.Code)
	}
	var views []dbServer.DocumentView
	json.Unmarshal(w.Body.Bytes(), &views)
	got := make([]string, len(views))
	for i, v := range views {
		got[i] = v.ID
	}
	if strings.Join(got, ",") != "a,b,c,d,e" {
		t.Fatalf("expected ordered a,b,c,d,e, got %v", got)
	}

	w = do(t, h, "GET", "/api/stores/db1/docs?range=b..d", "")
	views = nil
	json.Unmarshal(w.Body.Bytes(), &views)
	if len(views) != 3 || views[0].ID != "b" || views[2].ID != "d" {
		t.Fatalf("range b..d wrong: %d entries", len(views))
	}
}

func TestPostGeneratesID(t *testing.T) {
	h := newHandler(t)
	do(t, h, "PUT", "/api/stores/db1", "")
	w := do(t, h, "POST", "/api/stores/db1/docs", `{"name":"Ada","age":36}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("post expected 201, got %d", w.Code)
	}
	var body HrefResponseBody
	json.Unmarshal(w.Body.Bytes(), &body)
	if !strings.HasPrefix(body.Href, "/api/stores/db1/docs/doc-") {
		t.Fatalf("expected generated id href, got %q", body.Href)
	}
}

func TestPatchDocument(t *testing.T) {
	h := newHandler(t)
	do(t, h, "PUT", "/api/stores/db1", "")
	do(t, h, "PUT", "/api/stores/db1/docs/p1", `{"name":"Ada","age":36}`)

	// Add a new top-level field via RFC 6902 patch (still schema-valid).
	patchBody := `[{"op":"add","path":"/nickname","value":"Countess"}]`
	w := do(t, h, "PATCH", "/api/stores/db1/docs/p1", patchBody)
	if w.Code != http.StatusOK {
		t.Fatalf("patch expected 200, got %d (%s)", w.Code, w.Body.String())
	}

	w = do(t, h, "GET", "/api/stores/db1/docs/p1", "")
	if !strings.Contains(w.Body.String(), "Countess") {
		t.Fatalf("patched field missing: %s", w.Body.String())
	}
}

func TestInvalidPaths(t *testing.T) {
	h := newHandler(t)
	do(t, h, "PUT", "/api/stores/db1", "")
	cases := []struct {
		method, path string
	}{
		{"GET", "/api/stores/db1/docs/p1/collections/notes"}, // no nesting anymore
		{"GET", "/api/stores/db1/junk"},
		{"PATCH", "/api/stores/db1/docs"},
	}
	for _, c := range cases {
		if w := do(t, h, c.method, c.path, `{"name":"x","age":1}`); w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
			t.Fatalf("%s %s expected 4xx, got %d", c.method, c.path, w.Code)
		}
	}
}

func TestUnauthorized(t *testing.T) {
	h := newHandler(t)
	// No Authorization header.
	r := httptest.NewRequest("GET", "/api/stores/db1/docs", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth expected 401, got %d", w.Code)
	}
}

func TestConcurrentWrites(t *testing.T) {
	h := newHandler(t)
	do(t, h, "PUT", "/api/stores/db1", "")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "doc" + strings.Repeat("0", i%3) + itoa(i)
			do(t, h, "PUT", "/api/stores/db1/docs/"+id, `{"name":"x","age":1}`)
			do(t, h, "GET", "/api/stores/db1/docs/"+id, "")
		}(i)
	}
	wg.Wait()

	w := do(t, h, "GET", "/api/stores/db1/docs", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list after concurrent writes expected 200, got %d", w.Code)
	}
	body, _ := io.ReadAll(w.Result().Body)
	if len(body) == 0 {
		t.Fatal("empty list body")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
