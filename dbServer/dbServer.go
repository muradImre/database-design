// Package dbServer provides an HTTP server exposing a flat JSON document store.
// Each store holds documents addressed by id; nesting lives inside a document's
// JSON data rather than as separate collection resources.
package dbServer

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/muradImre/database-design/db"
	"github.com/muradImre/database-design/pair"
)

type AuthService interface {
	Login(w http.ResponseWriter, r *http.Request)
	Logout(w http.ResponseWriter, r *http.Request)
	ValidateToken(token string) (string, bool)
	HandlePreflight(w http.ResponseWriter, r *http.Request)
	GetUsernameFromRequest(r *http.Request) (string, error)
}

type Validator interface {
	ValidateDoc(document any) error
}

// DB is the storage contract for a single flat store.
type DB interface {
	WriteDocument(user, id string, data []byte, overwrite bool) (bool, error)
	ReadDocument(id string) (db.Document, error)
	ListDocuments(start, end string, ctx context.Context) ([]db.DocumentEntry, error)
	DeleteDocument(id string) error
}

// PatchApplier applies an RFC 6902 JSON Patch document to a JSON document. It
// takes and returns raw JSON bytes so the transport layer stays decoupled from
// the patch implementation's internal types.
type PatchApplier interface {
	Apply(document, patch []byte) (result []byte, err error)
}

type subscribers struct {
	mtx         sync.RWMutex
	subscribers map[string][]http.ResponseWriter
}

type DBIndex[K cmp.Ordered, V any] interface {
	Upsert(key K, check func(key K, currValue V, exists bool) (newValue V, err error)) (updated bool, err error)
	Remove(key K) (removedValue V, removed bool)
	Find(key K) (foundValue V, found bool)
	Query(ctx context.Context, start K, end K) (results []pair.Pair[K, V], err error)
}

type serverHandlers struct {
	databases    DBIndex[string, DB]
	dbFactory    DBFactory
	authManager  AuthService
	subscribers  subscribers
	validator    Validator
	patchApplier PatchApplier
	// onMutation, when set, is invoked after a mutating request succeeds so the
	// server can persist a snapshot. It is nil when persistence is disabled.
	onMutation func()
}

func (h *serverHandlers) mutated() {
	if h.onMutation != nil {
		h.onMutation()
	}
}

type AuthFactory interface {
	NewAuthManager() AuthService
}

type DBFactory func(name string) DB

type DatabasesFactory[K cmp.Ordered, V any] func() DBIndex[string, DB]

type ValidatorFactory interface {
	NewValidator() Validator
}

// maxKey is a sentinel upper bound used for open-ended range queries.
var maxKey = strings.Repeat("\U0010FFFF", 2000)

func handlePreflight(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, DELETE, POST, OPTIONS, PATCH")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.WriteHeader(http.StatusOK)
}

// parseRange parses the "range" query parameter of the form start..end,
// start.., ..end, .. or empty. It returns the resolved start and end keys.
func parseRange(rangeStr string) (start string, end string, ok bool) {
	if rangeStr == "" || rangeStr == ".." {
		return "", maxKey, true
	}
	idx := strings.Index(rangeStr, "..")
	if idx < 0 {
		return "", "", false
	}
	start = rangeStr[:idx]
	end = rangeStr[idx+2:]
	if end != "" && start > end {
		return "", "", false
	}
	if end == "" {
		end = maxKey
	}
	return start, end, true
}

func (h *serverHandlers) AuthorizationMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, DELETE, POST, OPTIONS, PATCH")

		if r.Method == "OPTIONS" {
			handlePreflight(w)
			return
		}

		token := r.Header.Get("Authorization")
		if len(token) > 7 && strings.HasPrefix(token, "Bearer ") {
			token = strings.TrimPrefix(token, "Bearer ")
		} else {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or malformed authorization header")
			return
		}

		if _, valid := h.authManager.ValidateToken(token); !valid {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
			return
		}

		next(w, r)
	}
}

func newMux(handlers *serverHandlers) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc(healthPath, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "OPTIONS":
			handlePreflight(w)
		case "GET":
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
	})

	mux.HandleFunc(sessionPath, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "OPTIONS":
			handlePreflight(w)
		case "POST":
			handlers.authManager.Login(w, r)
		case "DELETE":
			handlers.authManager.Logout(w, r)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
	})

	// GET /api/stores lists the names of all stores.
	mux.HandleFunc(storesListPath, handlers.AuthorizationMiddleware(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "OPTIONS":
			handlePreflight(w)
		case "GET":
			handlers.listStores(w, r)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
	}))

	mux.HandleFunc(storesPath, handlers.AuthorizationMiddleware(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "OPTIONS":
			handlePreflight(w)
		case "GET":
			handlers.handleGet(w, r)
		case "PUT":
			handlers.handlePut(w, r)
		case "POST":
			handlers.handlePost(w, r)
		case "PATCH":
			handlers.handlePatch(w, r)
		case "DELETE":
			handlers.handleDelete(w, r)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
	}))

	return mux
}

// listStores returns the names of every store, in order.
func (h *serverHandlers) listStores(w http.ResponseWriter, r *http.Request) {
	pairs, err := h.databases.Query(r.Context(), "", maxKey)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal", "error listing stores")
		return
	}
	names := make([]string, 0, len(pairs))
	for _, p := range pairs {
		names = append(names, p.Key)
	}
	writeJSON(w, http.StatusOK, map[string]any{"stores": names})
}

func newHandlers(authfac AuthFactory, dbfac DBFactory, valfac ValidatorFactory, dbsfac DatabasesFactory[string, DB], patchApplier PatchApplier) *serverHandlers {
	return &serverHandlers{
		databases:    dbsfac(),
		dbFactory:    dbfac,
		authManager:  authfac.NewAuthManager(),
		validator:    valfac.NewValidator(),
		patchApplier: patchApplier,
		subscribers: subscribers{
			subscribers: make(map[string][]http.ResponseWriter),
		},
	}
}

// New creates a plain HTTP server (no persistence) with the given factories.
func New(port int, authfac AuthFactory, dbfac DBFactory, dbsfac DatabasesFactory[string, DB], valfac ValidatorFactory, patchApplier PatchApplier) *http.Server {
	handlers := newHandlers(authfac, dbfac, valfac, dbsfac, patchApplier)
	return &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: newMux(handlers),
	}
}

func (h *serverHandlers) handleGet(w http.ResponseWriter, r *http.Request) {
	p, err := parseAPIPath(r.URL.Path)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid path")
		return
	}

	start, end, ok := parseRange(r.URL.Query().Get("range"))
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid range")
		return
	}

	watch := r.URL.Query().Get("watch")
	if watch == "1" || watch == "true" {
		h.subscribe(w, r, p, start, end)
		return
	}

	switch p.kind {
	case kindStore, kindDocList:
		h.listDocuments(w, r, p, start, end)
	case kindDocument:
		h.getDocument(w, r, p)
	default:
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid path")
	}
}

func (h *serverHandlers) handlePut(w http.ResponseWriter, r *http.Request) {
	p, err := parseAPIPath(r.URL.Path)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid path")
		return
	}

	switch p.kind {
	case kindStore:
		h.putStore(w, r, p)
	case kindDocument:
		h.putDocument(w, r, p)
	default:
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid path")
	}
}

func (h *serverHandlers) handlePost(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/json" {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid content type")
		return
	}

	p, err := parseAPIPath(r.URL.Path)
	if err != nil || p.kind != kindDocList {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid path")
		return
	}

	data, err := io.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "error reading request body")
		return
	}
	h.postDocument(w, r, p, data)
}

func (h *serverHandlers) handleDelete(w http.ResponseWriter, r *http.Request) {
	p, err := parseAPIPath(r.URL.Path)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid path")
		return
	}

	switch p.kind {
	case kindStore:
		h.deleteStore(w, r, p)
	case kindDocument:
		h.deleteDocument(w, r, p)
	default:
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid path")
	}
}

// toView builds a DocumentView from a stored document and its id.
func (h *serverHandlers) toView(document db.Document, id string) DocumentView {
	view := DocumentView{ID: id, Data: json.RawMessage(document.Data())}
	view.Metadata.CreatedAt = document.CreatedAt()
	view.Metadata.CreatedBy = document.CreatedBy()
	view.Metadata.UpdatedAt = document.UpdatedAt()
	view.Metadata.UpdatedBy = document.UpdatedBy()
	return view
}

func (h *serverHandlers) getDocument(w http.ResponseWriter, r *http.Request, p parsedPath) error {
	database, exists := h.databases.Find(p.store)
	if !exists {
		writeAPIError(w, http.StatusNotFound, "not_found", "store does not exist")
		return fmt.Errorf("store does not exist")
	}

	document, err := database.ReadDocument(p.docID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "document does not exist")
		return fmt.Errorf("document does not exist")
	}

	writeJSON(w, http.StatusOK, h.toView(document, p.docID))
	return nil
}

func (h *serverHandlers) putDocument(w http.ResponseWriter, r *http.Request, p parsedPath) {
	if r.Header.Get("Content-Type") != "application/json" {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid content type")
		return
	}

	data, err := io.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal", "error reading body")
		return
	}

	var parsedDoc any
	if err := json.Unmarshal(data, &parsedDoc); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid JSON format")
		return
	}
	if err := h.validator.ValidateDoc(parsedDoc); err != nil {
		slog.Error("Schema validation failed", "error", err)
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid document schema")
		return
	}

	database, exists := h.databases.Find(p.store)
	if !exists {
		writeAPIError(w, http.StatusNotFound, "not_found", "store does not exist")
		return
	}

	// if_exists defaults to "replace"; "fail" refuses to overwrite an existing document.
	overwrite := r.URL.Query().Get("if_exists") != "fail"
	_, readErr := database.ReadDocument(p.docID)
	existed := readErr == nil

	username, err := h.authManager.GetUsernameFromRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "unable to determine user")
		return
	}

	wrote, err := database.WriteDocument(username, p.docID, data, overwrite)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "error writing document")
		return
	}
	if !overwrite && !wrote {
		writeAPIError(w, http.StatusConflict, "conflict", "document already exists")
		return
	}

	status := http.StatusCreated
	if existed {
		status = http.StatusOK
	}
	writeHref(w, status, buildHref(p.store, p.docID))
	h.mutated()

	document, err := database.ReadDocument(p.docID)
	if err != nil {
		return
	}
	var docMap map[string]interface{}
	if err := json.Unmarshal(document.Data(), &docMap); err != nil {
		slog.Error("Error unmarshalling document data", "error", err)
		return
	}
	h.notify(p.store, p.docID, docMap, document, false)
}

// postDocument creates a document with a generated id under the store root.
func (h *serverHandlers) postDocument(w http.ResponseWriter, r *http.Request, p parsedPath, data []byte) {
	var parsedDoc any
	if err := json.Unmarshal(data, &parsedDoc); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid JSON format")
		return
	}
	if err := h.validator.ValidateDoc(parsedDoc); err != nil {
		slog.Error("Schema validation failed", "error", err)
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid document schema")
		return
	}

	database, exists := h.databases.Find(p.store)
	if !exists {
		writeAPIError(w, http.StatusNotFound, "not_found", "store does not exist")
		return
	}

	docID := "doc-" + fmt.Sprint(time.Now().UnixNano())

	username, err := h.authManager.GetUsernameFromRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "unable to determine user")
		return
	}

	if _, err := database.WriteDocument(username, docID, data, true); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal", "error writing document")
		return
	}

	writeHref(w, http.StatusCreated, buildHref(p.store, docID))
	h.mutated()

	document, err := database.ReadDocument(docID)
	if err == nil {
		var docMap map[string]interface{}
		if json.Unmarshal(document.Data(), &docMap) == nil {
			h.notify(p.store, docID, docMap, document, false)
		}
	}
}

// listDocuments lists the documents in a store within the requested range.
func (h *serverHandlers) listDocuments(w http.ResponseWriter, r *http.Request, p parsedPath, start, end string) error {
	database, exists := h.databases.Find(p.store)
	if !exists {
		writeAPIError(w, http.StatusNotFound, "not_found", "store does not exist")
		return nil
	}

	entries, err := database.ListDocuments(start, end, r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal", "error retrieving documents")
		return err
	}

	views := make([]DocumentView, len(entries))
	for i, e := range entries {
		views[i] = h.toView(e.Document, e.ID)
	}
	writeJSON(w, http.StatusOK, views)
	return nil
}

func (h *serverHandlers) putStore(w http.ResponseWriter, r *http.Request, p parsedPath) {
	if _, exists := h.databases.Find(p.store); exists {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "store already exists")
		return
	}

	newDB := h.dbFactory(p.store)
	_, err := h.databases.Upsert(p.store, func(key string, curr DB, exists bool) (DB, error) {
		if exists {
			return nil, fmt.Errorf("store already exists")
		}
		return newDB, nil
	})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "error creating store")
		return
	}

	writeHref(w, http.StatusCreated, buildHref(p.store, ""))
	h.mutated()
}

func (h *serverHandlers) deleteStore(w http.ResponseWriter, r *http.Request, p parsedPath) {
	if _, exists := h.databases.Find(p.store); !exists {
		writeAPIError(w, http.StatusNotFound, "not_found", "store does not exist")
		return
	}
	h.databases.Remove(p.store)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusNoContent)
	h.mutated()
}

func (h *serverHandlers) deleteDocument(w http.ResponseWriter, r *http.Request, p parsedPath) {
	database, exists := h.databases.Find(p.store)
	if !exists {
		writeAPIError(w, http.StatusNotFound, "not_found", "store does not exist")
		return
	}

	document, err := database.ReadDocument(p.docID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "document does not exist")
		return
	}

	if err := database.DeleteDocument(p.docID); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
		return
	}

	h.notify(p.store, p.docID, nil, document, true)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusNoContent)
	h.mutated()
}

// handlePatch applies a sequence of patch operations to a document's JSON.
func (h *serverHandlers) handlePatch(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/json" {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid content type")
		return
	}

	p, err := parseAPIPath(r.URL.Path)
	if err != nil || p.kind != kindDocument {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid path")
		return
	}

	database, exists := h.databases.Find(p.store)
	if !exists {
		writeAPIError(w, http.StatusNotFound, "not_found", "store does not exist")
		return
	}

	document, err := database.ReadDocument(p.docID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "document does not exist")
		return
	}

	patchBytes, err := io.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal", "error reading body")
		return
	}

	href := buildHref(p.store, p.docID)

	finalData, err := h.patchApplier.Apply(document.Data(), patchBytes)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, patchResult{Href: href, Applied: false, Detail: err.Error()})
		return
	}

	var patched any
	if err := json.Unmarshal(finalData, &patched); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal", "error encoding patched document")
		return
	}
	if err := h.validator.ValidateDoc(patched); err != nil {
		slog.Error("Schema validation failed after patch operations", "error", err)
		writeAPIError(w, http.StatusBadRequest, "bad_request", "patch caused schema violation: "+err.Error())
		return
	}

	username, err := h.authManager.GetUsernameFromRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "unable to determine user")
		return
	}
	if _, err := database.WriteDocument(username, p.docID, finalData, true); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal", "error saving patched document")
		return
	}

	updated, _ := database.ReadDocument(p.docID)
	var docMap map[string]interface{}
	_ = json.Unmarshal(finalData, &docMap)
	h.notify(p.store, p.docID, docMap, updated, false)

	writeJSON(w, http.StatusOK, patchResult{Href: href, Applied: true, Detail: "patch applied"})
	h.mutated()
}

// subKey builds the subscribers map key for a document ("" docID = store level).
func subKey(store, docID string) string {
	if docID == "" {
		return "store:" + store
	}
	return "doc:" + store + "/" + docID
}

// subscribe opens a server-sent event stream for a store listing or a document.
func (h *serverHandlers) subscribe(w http.ResponseWriter, r *http.Request, p parsedPath, start, end string) {
	type writeFlusher interface {
		http.ResponseWriter
		http.Flusher
	}
	wf, ok := w.(writeFlusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}

	wf.Header().Set("Content-Type", "text/event-stream")
	wf.Header().Set("Cache-Control", "no-cache")
	wf.Header().Set("Connection", "keep-alive")
	wf.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Last-Event-ID")
	wf.Header().Set("Access-Control-Allow-Origin", "*")
	wf.WriteHeader(http.StatusOK)
	wf.Flush()

	switch p.kind {
	case kindDocument:
		if err := h.getDocument(w, r, p); err != nil {
			return
		}
		h.addSubscriber(subKey(p.store, p.docID), wf)
	case kindStore, kindDocList:
		if err := h.listDocuments(w, r, p, start, end); err != nil {
			return
		}
		h.addSubscriber(subKey(p.store, ""), wf)
	default:
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(15 * time.Second):
			wf.Write([]byte(": keep-alive\n\n"))
			wf.Flush()
		}
	}
}

// notify pushes a change to document- and store-level subscribers.
func (h *serverHandlers) notify(store, docID string, docMap map[string]interface{}, document db.Document, isDelete bool) {
	eventID := time.Now().UnixMilli()
	eventType := "update"

	var eventData string
	if isDelete {
		eventType = "delete"
		eventData = fmt.Sprintf(`"%s"`, docID)
	} else {
		payload, err := json.Marshal(map[string]interface{}{
			"id":   docID,
			"data": docMap,
			"metadata": map[string]interface{}{
				"created_at": document.CreatedAt(),
				"created_by": document.CreatedBy(),
				"updated_at": document.UpdatedAt(),
				"updated_by": document.UpdatedBy(),
			},
		})
		if err != nil {
			slog.Error("Error encoding document data for SSE", "error", err)
			return
		}
		eventData = string(payload)
	}

	eventString := fmt.Sprintf("event: %s\ndata: %s\nid: %d\n\n", eventType, eventData, eventID)
	for _, key := range []string{subKey(store, docID), subKey(store, "")} {
		for _, sub := range h.getSubscribers(key) {
			sub.Write([]byte(eventString))
			sub.(http.Flusher).Flush()
		}
	}
}

func (h *serverHandlers) getSubscribers(key string) []http.ResponseWriter {
	h.subscribers.mtx.RLock()
	defer h.subscribers.mtx.RUnlock()
	return h.subscribers.subscribers[key]
}

func (h *serverHandlers) addSubscriber(key string, wf http.ResponseWriter) {
	h.subscribers.mtx.Lock()
	defer h.subscribers.mtx.Unlock()
	if h.subscribers.subscribers == nil {
		h.subscribers.subscribers = make(map[string][]http.ResponseWriter)
	}
	h.subscribers.subscribers[key] = append(h.subscribers.subscribers[key], wf)
}
