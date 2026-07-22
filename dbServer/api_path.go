package dbServer

import (
	"fmt"
	"strings"
)

const (
	apiPrefix   = "/api"
	storesRoot  = "stores"
	docsSeg     = "docs"
	sessionPath    = "/api/session"
	storesPath     = "/api/stores/"
	storesListPath = "/api/stores"
	healthPath     = "/api/health"
)

// resourceKind identifies what a parsed API path refers to.
type resourceKind int

const (
	kindInvalid  resourceKind = iota
	kindStore                 // /api/stores/{store}
	kindDocList               // /api/stores/{store}/docs
	kindDocument              // /api/stores/{store}/docs/{id}
)

// parsedPath is a typed view of an API path under /api/stores/.
type parsedPath struct {
	store string
	docID string
	kind  resourceKind
}

// parseAPIPath parses URLs of the form:
//
//	/api/stores/{store}
//	/api/stores/{store}/docs
//	/api/stores/{store}/docs/{id}
func parseAPIPath(raw string) (parsedPath, error) {
	path := strings.TrimSuffix(raw, "/")
	parts := strings.Split(strings.Trim(path, "/"), "/")

	if len(parts) < 3 || parts[0] != "api" || parts[1] != storesRoot || parts[2] == "" {
		return parsedPath{}, fmt.Errorf("invalid path")
	}

	p := parsedPath{store: parts[2]}
	rest := parts[3:]

	switch len(rest) {
	case 0:
		p.kind = kindStore
	case 1:
		if rest[0] != docsSeg {
			return parsedPath{}, fmt.Errorf("invalid path")
		}
		p.kind = kindDocList
	case 2:
		if rest[0] != docsSeg || rest[1] == "" {
			return parsedPath{}, fmt.Errorf("invalid path")
		}
		p.kind = kindDocument
		p.docID = rest[1]
	default:
		return parsedPath{}, fmt.Errorf("invalid path")
	}
	return p, nil
}

// buildHref builds the public href for a store or document resource.
func buildHref(store, docID string) string {
	var b strings.Builder
	b.WriteString(apiPrefix)
	b.WriteString("/")
	b.WriteString(storesRoot)
	b.WriteString("/")
	b.WriteString(store)
	if docID != "" {
		b.WriteString("/")
		b.WriteString(docsSeg)
		b.WriteString("/")
		b.WriteString(docID)
	}
	return b.String()
}
