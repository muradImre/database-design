package dbServer

import (
	"encoding/json"
	"net/http"
)

// apiError is the standard error envelope.
type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// resourceCreated is returned for create/update resource operations.
type resourceCreated struct {
	Href string `json:"href"`
}

// DocumentView is returned for document reads. Data carries the document's
// JSON body verbatim.
type DocumentView struct {
	ID       string          `json:"id"`
	Data     json.RawMessage `json:"data"`
	Metadata struct {
		CreatedAt int64  `json:"created_at"`
		CreatedBy string `json:"created_by"`
		UpdatedAt int64  `json:"updated_at"`
		UpdatedBy string `json:"updated_by"`
	} `json:"metadata"`
}

// patchResult is returned for patch operations.
type patchResult struct {
	Href    string `json:"href"`
	Applied bool   `json:"applied"`
	Detail  string `json:"detail"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	var body apiError
	body.Error.Code = code
	body.Error.Message = message
	writeJSON(w, status, body)
}

func writeHref(w http.ResponseWriter, status int, href string) {
	w.Header().Set("Location", href)
	writeJSON(w, status, resourceCreated{Href: href})
}
