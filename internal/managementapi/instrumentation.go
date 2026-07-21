package managementapi

import (
	"net/http"
	"strings"
	"time"
)

const unknownManagementRoute = "/api/v1/unknown"

// Observer receives bounded HTTP outcomes. Route, method, result, and update
// result are normalized by the handler before this boundary is invoked.
type Observer interface {
	Request(route, method, result string, elapsed time.Duration)
	ConfigUpdate(result string)
}

type noopObserver struct{}

func (noopObserver) Request(string, string, string, time.Duration) {}
func (noopObserver) ConfigUpdate(string)                           {}

type responseObserver struct {
	http.ResponseWriter
	status int
}

func (response *responseObserver) WriteHeader(status int) {
	if response.status != 0 {
		return
	}
	response.status = status
	response.ResponseWriter.WriteHeader(status)
}

func (response *responseObserver) Write(contents []byte) (int, error) {
	if response.status == 0 {
		response.WriteHeader(http.StatusOK)
	}
	return response.ResponseWriter.Write(contents)
}

func normalizedManagementRoute(path string) string {
	switch path {
	case "/api/v1/status", "/api/v1/config", "/api/v1/config/validate", "/api/v1/config/pending",
		interfacesPath, midiDevicesPath, midiAuditionPath, midiPanicPath,
		rulesCollectionPath, "/api/v1/flows", "/api/v1/flows/mute", "/api/v1/flows/solo":
		return path
	default:
		if strings.HasPrefix(path, rulesCollectionPath+"/") {
			return rulesCollectionPath + "/{id}"
		}
		return unknownManagementRoute
	}
}

func normalizedManagementMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	default:
		return "OTHER"
	}
}

func managementRequestResult(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "success"
	case status >= 400 && status < 500:
		return "client_error"
	case status >= 500 && status < 600:
		return "server_error"
	default:
		return "other"
	}
}

func configUpdateResult(status int) string {
	switch status {
	case http.StatusOK:
		return "success"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusPreconditionFailed, http.StatusPreconditionRequired:
		return "precondition"
	case http.StatusConflict:
		return "conflict"
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return "invalid"
	case http.StatusServiceUnavailable:
		return "unavailable"
	default:
		return "error"
	}
}
