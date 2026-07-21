package managementapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
)

const rulesCollectionPath = "/api/v1/rules"

var errMissingRuleItem = errors.New("rule item path is missing an ID")

// RulesDocument is the complete ordered persistent rule collection at one
// configuration revision.
type RulesDocument struct {
	Revision Revision           `json:"revision"`
	Writable bool               `json:"writable"`
	Rules    config.RulesConfig `json:"rules"`
}

type reorderRulesRequest struct {
	Order []string `json:"order"`
}

type replaceRulesRequest struct {
	Rules config.RulesConfig `json:"rules"`
}

func (handler *handler) serveRulesCollection(response http.ResponseWriter, request *http.Request) {
	if rejectRulesQuery(response, request) {
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handler.listRules(response, request)
	case http.MethodPost:
		handler.createRule(response, request)
	case http.MethodPut:
		handler.replaceRules(response, request)
	case http.MethodPatch:
		handler.reorderRules(response, request)
	default:
		methodNotAllowed(response, request, "GET, HEAD, POST, PUT, PATCH")
	}
}

func (handler *handler) replaceRules(response http.ResponseWriter, request *http.Request) {
	expected, ok := requestRevision(response, request)
	if !ok {
		return
	}
	var body replaceRulesRequest
	if !decodeJSONRequest(response, request, &body) {
		return
	}
	if body.Rules == nil {
		writeProblem(response, request, http.StatusUnprocessableEntity, "invalid_rule", "rules must be an array", []string{"rules"})
		return
	}
	document, err := handler.backend.ReplaceRules(request.Context(), expected, body.Rules)
	if err != nil {
		writeBackendError(response, request, err)
		return
	}
	writeRulesDocument(response, request, http.StatusOK, document, "")
}

func (handler *handler) serveRuleItem(response http.ResponseWriter, request *http.Request, escapedPath string) {
	if rejectRulesQuery(response, request) {
		return
	}
	id, err := parseRuleItemID(escapedPath)
	if errors.Is(err, errMissingRuleItem) {
		writeProblem(response, request, http.StatusNotFound, "not_found", "the requested management API route does not exist", nil)
		return
	}
	if err != nil {
		writeProblem(response, request, http.StatusBadRequest, "invalid_rule_id", "the rule ID path component is malformed", []string{"id"})
		return
	}
	switch request.Method {
	case http.MethodPut:
		handler.replaceRule(response, request, id)
	case http.MethodDelete:
		handler.deleteRule(response, request, id)
	default:
		methodNotAllowed(response, request, "PUT, DELETE")
	}
}

func (handler *handler) listRules(response http.ResponseWriter, request *http.Request) {
	document, err := handler.backend.Rules(request.Context())
	if err != nil {
		writeBackendError(response, request, err)
		return
	}
	writeRulesDocument(response, request, http.StatusOK, document, "")
}

func (handler *handler) createRule(response http.ResponseWriter, request *http.Request) {
	expected, ok := requestRevision(response, request)
	if !ok {
		return
	}
	var rule config.RuleConfig
	if !decodeJSONRequest(response, request, &rule) {
		return
	}
	document, err := handler.backend.CreateRule(request.Context(), expected, rule)
	if err != nil {
		writeBackendError(response, request, err)
		return
	}
	writeRulesDocument(response, request, http.StatusCreated, document, rulesCollectionPath+"/"+escapeRuleID(rule.ID))
}

func (handler *handler) reorderRules(response http.ResponseWriter, request *http.Request) {
	expected, ok := requestRevision(response, request)
	if !ok {
		return
	}
	var body reorderRulesRequest
	if !decodeJSONRequest(response, request, &body) {
		return
	}
	document, err := handler.backend.ReorderRules(request.Context(), expected, body.Order)
	if err != nil {
		writeBackendError(response, request, err)
		return
	}
	writeRulesDocument(response, request, http.StatusOK, document, "")
}

func (handler *handler) replaceRule(response http.ResponseWriter, request *http.Request, id string) {
	expected, ok := requestRevision(response, request)
	if !ok {
		return
	}
	var rule config.RuleConfig
	if !decodeJSONRequest(response, request, &rule) {
		return
	}
	document, err := handler.backend.ReplaceRule(request.Context(), expected, id, rule)
	if err != nil {
		writeBackendError(response, request, err)
		return
	}
	writeRulesDocument(response, request, http.StatusOK, document, "")
}

func (handler *handler) deleteRule(response http.ResponseWriter, request *http.Request, id string) {
	expected, ok := requestRevision(response, request)
	if !ok {
		return
	}
	document, err := handler.backend.DeleteRule(request.Context(), expected, id)
	if err != nil {
		writeBackendError(response, request, err)
		return
	}
	writeRulesDocument(response, request, http.StatusOK, document, "")
}

func requestRevision(response http.ResponseWriter, request *http.Request) (Revision, bool) {
	expected, err := parseIfMatch(request.Header.Values("If-Match"))
	if err == nil {
		return expected, true
	}
	status := http.StatusBadRequest
	code := "invalid_if_match"
	if errors.Is(err, errMissingIfMatch) {
		status = http.StatusPreconditionRequired
		code = "precondition_required"
	}
	writeProblem(response, request, status, code, err.Error(), nil)
	return "", false
}

func writeRulesDocument(response http.ResponseWriter, request *http.Request, status int, document RulesDocument, location string) {
	etag, ok := formatETag(document.Revision)
	if !ok {
		writeProblem(response, request, http.StatusInternalServerError, "internal_error", "backend returned an invalid rules revision", nil)
		return
	}
	response.Header().Set("ETag", etag)
	if location != "" {
		response.Header().Set("Location", location)
	}
	writeJSON(response, request, status, document)
}

func parseRuleItemID(escapedPath string) (string, error) {
	escapedID, ok := strings.CutPrefix(escapedPath, rulesCollectionPath+"/")
	if !ok || escapedID == "" {
		return "", errMissingRuleItem
	}
	if strings.Contains(escapedID, "/") {
		return "", errors.New("rule item path contains multiple components")
	}
	id, err := url.PathUnescape(escapedID)
	if err != nil || id == "" || !utf8.ValidString(id) {
		return "", errors.New("rule item path component is invalid")
	}
	return id, nil
}

func escapeRuleID(id string) string {
	escaped := url.PathEscape(id)
	switch escaped {
	case ".":
		return "%2E"
	case "..":
		return "%2E%2E"
	default:
		return escaped
	}
}

func rejectRulesQuery(response http.ResponseWriter, request *http.Request) bool {
	if request.URL.RawQuery == "" && !request.URL.ForceQuery && request.URL.Fragment == "" {
		return false
	}
	writeProblem(response, request, http.StatusBadRequest, "invalid_query", "rule routes do not accept a query string or fragment", nil)
	return true
}
