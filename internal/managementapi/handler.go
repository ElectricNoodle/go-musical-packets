// Package managementapi provides the transport boundary for the local
// management API. Runtime behavior remains behind Backend so this package has
// no dependency on application composition or native adapters.
package managementapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
)

// Status is the transport-neutral runtime status returned by Backend.
type Status struct {
	State    string   `json:"state"`
	Revision Revision `json:"revision"`
	Writable bool     `json:"writable"`
	Warning  string   `json:"warning,omitempty"`
}

// Revision is an opaque HTTP validator supplied by Backend.
type Revision string

// String returns the revision's wire representation.
func (revision Revision) String() string { return string(revision) }

// ConfigDocument identifies a configuration representation and its revision.
type ConfigDocument struct {
	Config   config.Config
	Revision Revision
}

// Validation identifies the active revision against which a candidate was
// classified and describes which fields can be applied now or require restart.
type Validation struct {
	Revision              Revision `json:"revision"`
	HotFields             []string `json:"hot_fields"`
	RestartRequiredFields []string `json:"restart_required_fields"`
}

// Backend owns runtime configuration policy. Implementations must return
// detached values that callers may safely inspect.
type Backend interface {
	Status(context.Context) (Status, error)
	Config(context.Context) (ConfigDocument, error)
	ValidateConfig(context.Context, config.Config) (Validation, error)
	UpdateConfig(context.Context, Revision, config.Config) (ConfigDocument, error)
	Flows(context.Context, FlowPageRequest) (FlowPage, error)
	SetMutedFlows(context.Context, []string) (FlowOverlay, error)
	SetSoloedFlows(context.Context, []string) (FlowOverlay, error)
}

// ErrorKind classifies errors returned across the Backend boundary.
type ErrorKind string

const (
	ErrorInvalid            ErrorKind = "invalid"
	ErrorPreconditionFailed ErrorKind = "precondition_failed"
	ErrorConflict           ErrorKind = "conflict"
	ErrorUnavailable        ErrorKind = "unavailable"
)

// BackendError is a stable error contract between runtime policy and HTTP.
// Code is suitable for programmatic clients; Detail and Fields are safe to
// expose to a local user.
type BackendError struct {
	Kind           ErrorKind
	Code           string
	Detail         string
	Fields         []string
	ActualRevision Revision
	Err            error
}

func (backendError *BackendError) Error() string {
	if backendError == nil {
		return "<nil>"
	}
	if backendError.Detail != "" {
		return backendError.Detail
	}
	if backendError.Err != nil {
		return backendError.Err.Error()
	}
	if backendError.Code != "" {
		return backendError.Code
	}
	return string(backendError.Kind)
}

// Unwrap makes BackendError compatible with errors.Is and errors.As.
func (backendError *BackendError) Unwrap() error {
	if backendError == nil {
		return nil
	}
	return backendError.Err
}

// Options binds request authority checks to the actual listener port.
type Options struct {
	AllowedPort uint16
}

// NewHandler constructs the local-only management API.
func NewHandler(backend Backend, options Options) (http.Handler, error) {
	if backend == nil || nilBackend(backend) {
		return nil, errors.New("management API backend is required")
	}
	if options.AllowedPort == 0 {
		return nil, errors.New("management API allowed port is required")
	}
	return &handler{backend: backend, allowedPort: options.AllowedPort}, nil
}

func nilBackend(backend Backend) bool {
	value := reflect.ValueOf(backend)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

type handler struct {
	backend     Backend
	allowedPort uint16
}

func (handler *handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	setResponseSecurityHeaders(response.Header())
	if !localRequest(request, handler.allowedPort) {
		writeProblem(response, request, http.StatusForbidden, "forbidden", "management API requests must use a matching local origin", nil)
		return
	}

	switch request.URL.EscapedPath() {
	case "/api/v1/status":
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			methodNotAllowed(response, request, "GET, HEAD")
			return
		}
		handler.status(response, request)
	case "/api/v1/config":
		switch request.Method {
		case http.MethodGet, http.MethodHead:
			handler.config(response, request)
		case http.MethodPut:
			handler.updateConfig(response, request)
		default:
			methodNotAllowed(response, request, "GET, HEAD, PUT")
		}
	case "/api/v1/config/validate":
		if request.Method != http.MethodPost {
			methodNotAllowed(response, request, "POST")
			return
		}
		handler.validateConfig(response, request)
	case "/api/v1/flows":
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			methodNotAllowed(response, request, "GET, HEAD")
			return
		}
		handler.flows(response, request)
	case "/api/v1/flows/mute":
		if request.Method != http.MethodPost {
			methodNotAllowed(response, request, "POST")
			return
		}
		handler.setMutedFlows(response, request)
	case "/api/v1/flows/solo":
		if request.Method != http.MethodPost {
			methodNotAllowed(response, request, "POST")
			return
		}
		handler.setSoloedFlows(response, request)
	default:
		writeProblem(response, request, http.StatusNotFound, "not_found", "the requested management API route does not exist", nil)
	}
}

func (handler *handler) status(response http.ResponseWriter, request *http.Request) {
	status, err := handler.backend.Status(request.Context())
	if err != nil {
		writeBackendError(response, request, err)
		return
	}
	writeJSON(response, request, http.StatusOK, status)
}

func (handler *handler) config(response http.ResponseWriter, request *http.Request) {
	document, err := handler.backend.Config(request.Context())
	if err != nil {
		writeBackendError(response, request, err)
		return
	}
	writeConfig(response, request, document)
}

func (handler *handler) validateConfig(response http.ResponseWriter, request *http.Request) {
	configuration, ok := decodeConfigRequest(response, request)
	if !ok {
		return
	}
	validation, err := handler.backend.ValidateConfig(request.Context(), configuration)
	if err != nil {
		writeBackendError(response, request, err)
		return
	}
	writeJSON(response, request, http.StatusOK, validation)
}

func (handler *handler) updateConfig(response http.ResponseWriter, request *http.Request) {
	expected, err := parseIfMatch(request.Header.Values("If-Match"))
	if err != nil {
		status := http.StatusBadRequest
		code := "invalid_if_match"
		if errors.Is(err, errMissingIfMatch) {
			status = http.StatusPreconditionRequired
			code = "precondition_required"
		}
		writeProblem(response, request, status, code, err.Error(), nil)
		return
	}
	configuration, ok := decodeConfigRequest(response, request)
	if !ok {
		return
	}
	document, err := handler.backend.UpdateConfig(request.Context(), expected, configuration)
	if err != nil {
		writeBackendError(response, request, err)
		return
	}
	writeConfig(response, request, document)
}

func decodeConfigRequest(response http.ResponseWriter, request *http.Request) (config.Config, bool) {
	contentEncodings := request.Header.Values("Content-Encoding")
	if len(contentEncodings) > 1 || len(contentEncodings) == 1 && !strings.EqualFold(strings.TrimSpace(contentEncodings[0]), "identity") {
		writeProblem(response, request, http.StatusUnsupportedMediaType, "unsupported_content_encoding", "Content-Encoding must be absent or identity", nil)
		return config.Config{}, false
	}
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		writeProblem(response, request, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/yaml", nil)
		return config.Config{}, false
	}
	mediaType, parameters, err := mime.ParseMediaType(contentTypes[0])
	validParameters := len(parameters) == 0 || len(parameters) == 1 && strings.EqualFold(parameters["charset"], "utf-8")
	if err != nil || !strings.EqualFold(mediaType, "application/yaml") || !validParameters {
		writeProblem(response, request, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/yaml", nil)
		return config.Config{}, false
	}

	contents, err := io.ReadAll(http.MaxBytesReader(response, request.Body, config.MaximumBytes))
	if err != nil {
		var maximumBytesError *http.MaxBytesError
		if errors.As(err, &maximumBytesError) {
			writeProblem(response, request, http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("configuration body exceeds %d bytes", config.MaximumBytes), nil)
			return config.Config{}, false
		}
		writeProblem(response, request, http.StatusBadRequest, "invalid_body", "could not read the request body", nil)
		return config.Config{}, false
	}
	configuration, err := config.Decode(bytes.NewReader(contents))
	if err != nil {
		writeProblem(response, request, http.StatusUnprocessableEntity, "invalid_config", err.Error(), nil)
		return config.Config{}, false
	}
	return configuration, true
}

func writeConfig(response http.ResponseWriter, request *http.Request, document ConfigDocument) {
	contents, err := config.Encode(document.Config)
	if err != nil {
		writeProblem(response, request, http.StatusInternalServerError, "internal_error", "backend returned an invalid configuration", nil)
		return
	}
	etag, ok := formatETag(document.Revision)
	if !ok {
		writeProblem(response, request, http.StatusInternalServerError, "internal_error", "backend returned an invalid configuration revision", nil)
		return
	}
	response.Header().Set("Content-Type", "application/yaml")
	response.Header().Set("ETag", etag)
	response.Header().Set("Content-Length", strconv.Itoa(len(contents)))
	response.WriteHeader(http.StatusOK)
	if request.Method != http.MethodHead {
		_, _ = response.Write(contents)
	}
}

func writeJSON(response http.ResponseWriter, request *http.Request, status int, value any) {
	contents, err := json.Marshal(value)
	if err != nil {
		writeProblem(response, request, http.StatusInternalServerError, "internal_error", "response could not be encoded", nil)
		return
	}
	contents = append(contents, '\n')
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Content-Length", strconv.Itoa(len(contents)))
	response.WriteHeader(status)
	if request.Method != http.MethodHead {
		_, _ = response.Write(contents)
	}
}

type problem struct {
	Type   string   `json:"type"`
	Title  string   `json:"title"`
	Status int      `json:"status"`
	Code   string   `json:"code"`
	Detail string   `json:"detail"`
	Fields []string `json:"fields,omitempty"`
}

func writeProblem(response http.ResponseWriter, request *http.Request, status int, code, detail string, fields []string) {
	value := problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Code:   code,
		Detail: detail,
		Fields: append([]string(nil), fields...),
	}
	contents, err := json.Marshal(value)
	if err != nil {
		contents = []byte(`{"type":"about:blank","title":"Internal Server Error","status":500,"code":"internal_error","detail":"response could not be encoded"}`)
		status = http.StatusInternalServerError
	}
	contents = append(contents, '\n')
	response.Header().Set("Content-Type", "application/problem+json")
	response.Header().Set("Content-Length", strconv.Itoa(len(contents)))
	response.WriteHeader(status)
	if request.Method != http.MethodHead {
		_, _ = response.Write(contents)
	}
}

func writeBackendError(response http.ResponseWriter, request *http.Request, err error) {
	var backendError *BackendError
	if !errors.As(err, &backendError) || backendError == nil {
		writeProblem(response, request, http.StatusInternalServerError, "internal_error", "the management operation failed", nil)
		return
	}

	status := http.StatusInternalServerError
	defaultCode := "internal_error"
	defaultDetail := "the management operation failed"
	switch backendError.Kind {
	case ErrorInvalid:
		status = http.StatusUnprocessableEntity
		defaultCode = "invalid"
		defaultDetail = "the management request is invalid"
	case ErrorPreconditionFailed:
		status = http.StatusPreconditionFailed
		defaultCode = "precondition_failed"
		defaultDetail = "the request precondition does not match current state"
	case ErrorConflict:
		status = http.StatusConflict
		defaultCode = "conflict"
		defaultDetail = "the management request conflicts with current state"
	case ErrorUnavailable:
		status = http.StatusServiceUnavailable
		defaultCode = "unavailable"
		defaultDetail = "the management service is temporarily unavailable"
	}
	code := backendError.Code
	if code == "" {
		code = defaultCode
	}
	detail := backendError.Detail
	if detail == "" || status >= 500 {
		detail = defaultDetail
	}
	if status == http.StatusInternalServerError {
		code = "internal_error"
	}
	if backendError.ActualRevision != "" {
		etag, ok := formatETag(backendError.ActualRevision)
		if !ok {
			writeProblem(response, request, http.StatusInternalServerError, "internal_error", "the management operation failed", nil)
			return
		}
		response.Header().Set("ETag", etag)
	}
	writeProblem(response, request, status, code, detail, backendError.Fields)
}

func methodNotAllowed(response http.ResponseWriter, request *http.Request, allow string) {
	response.Header().Set("Allow", allow)
	writeProblem(response, request, http.StatusMethodNotAllowed, "method_not_allowed", "the request method is not allowed for this route", nil)
}

func setResponseSecurityHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
}

func localRequest(request *http.Request, allowedPort uint16) bool {
	remoteHost, remotePort, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil || remotePort == "" {
		return false
	}
	port, err := strconv.ParseUint(remotePort, 10, 16)
	if err != nil || port == 0 {
		return false
	}
	remoteIP := net.ParseIP(remoteHost)
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	if remoteIP == nil || !remoteIP.IsLoopback() || !loopbackAuthority(request.Host, scheme, allowedPort) {
		return false
	}

	origins, present := request.Header["Origin"]
	if !present {
		return true
	}
	if len(origins) != 1 || origins[0] == "" {
		return false
	}
	origin, err := url.Parse(origins[0])
	if err != nil || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" || origin.Scheme != scheme || !loopbackAuthority(origin.Host, scheme, allowedPort) {
		return false
	}
	return origins[0] == scheme+"://"+request.Host
}

func loopbackAuthority(value, scheme string, allowedPort uint16) bool {
	if value == "" || strings.ContainsAny(value, " \t\r\n/@") {
		return false
	}
	host := value
	portText := ""
	if parsedHost, parsedPort, err := net.SplitHostPort(value); err == nil {
		if parsedPort == "" {
			return false
		}
		host = parsedHost
		portText = parsedPort
	} else if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
	} else if strings.Contains(value, ":") {
		return false
	}
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	if portText == "" {
		return scheme == "http" && allowedPort == 80 || scheme == "https" && allowedPort == 443
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	return err == nil && port != 0 && uint16(port) == allowedPort
}

var errMissingIfMatch = errors.New("If-Match header is required")

func parseIfMatch(values []string) (Revision, error) {
	if len(values) == 0 {
		return "", errMissingIfMatch
	}
	if len(values) != 1 {
		return "", errors.New("If-Match must contain exactly one strong entity tag")
	}
	value := strings.TrimSpace(values[0])
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' || strings.HasPrefix(value, "W/") {
		return "", errors.New("If-Match must contain exactly one strong quoted revision")
	}
	opaque := value[1 : len(value)-1]
	if !validRevision(opaque) {
		return "", errors.New("If-Match contains an invalid revision")
	}
	return Revision(opaque), nil
}

func formatETag(revision Revision) (string, bool) {
	opaque := revision.String()
	if !validRevision(opaque) {
		return "", false
	}
	return fmt.Sprintf("\"%s\"", opaque), true
}

func validRevision(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}
