package managementapi

import (
	"net/http"
	"net/netip"
	"unicode/utf8"
)

const interfacesPath = "/api/v1/interfaces"

// CaptureInterface is one packet-capture device in a point-in-time discovery
// result. Addresses are canonical IP prefixes rather than host-only strings.
type CaptureInterface struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Addresses   []string `json:"addresses"`
	Up          bool     `json:"up"`
	Loopback    bool     `json:"loopback"`
}

// InterfacesDocument describes current capture-device discovery and how the
// active configuration resolves against it. Selected is empty when no current
// device satisfies the configured selection.
type InterfacesDocument struct {
	Configured string             `json:"configured"`
	Selected   string             `json:"selected"`
	Interfaces []CaptureInterface `json:"interfaces"`
}

func (handler *handler) serveInterfaces(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		methodNotAllowed(response, request, "GET, HEAD")
		return
	}
	if request.URL.RawQuery != "" || request.URL.ForceQuery || request.URL.Fragment != "" {
		writeProblem(response, request, http.StatusBadRequest, "invalid_query", "interface discovery does not accept a query string or fragment", nil)
		return
	}
	document, err := handler.backend.Interfaces(request.Context())
	if err != nil {
		writeBackendError(response, request, err)
		return
	}
	if !validInterfacesDocument(document) {
		writeProblem(response, request, http.StatusInternalServerError, "internal_error", "backend returned invalid capture interface state", nil)
		return
	}
	writeJSON(response, request, http.StatusOK, document)
}

func validInterfacesDocument(document InterfacesDocument) bool {
	if document.Configured == "" || !utf8.ValidString(document.Configured) || !utf8.ValidString(document.Selected) || document.Interfaces == nil {
		return false
	}
	seen := make(map[string]struct{}, len(document.Interfaces))
	selectedFound := document.Selected == ""
	for _, candidate := range document.Interfaces {
		if candidate.Name == "" || !utf8.ValidString(candidate.Name) || !utf8.ValidString(candidate.Description) || candidate.Addresses == nil {
			return false
		}
		if _, duplicate := seen[candidate.Name]; duplicate {
			return false
		}
		seen[candidate.Name] = struct{}{}
		if candidate.Name == document.Selected {
			selectedFound = true
		}
		addressSeen := make(map[string]struct{}, len(candidate.Addresses))
		for _, address := range candidate.Addresses {
			prefix, err := netip.ParsePrefix(address)
			if err != nil || prefix.String() != address {
				return false
			}
			if _, duplicate := addressSeen[address]; duplicate {
				return false
			}
			addressSeen[address] = struct{}{}
		}
	}
	return selectedFound
}
