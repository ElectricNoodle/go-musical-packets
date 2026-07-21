package managementapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

const (
	midiDevicesPath              = "/api/v1/midi/devices"
	midiAuditionPath             = "/api/v1/midi/audition"
	midiPanicPath                = "/api/v1/midi/panic"
	maximumMIDIAuditionBodyBytes = 4 << 10
)

// MIDIDiscoveryState is the bounded result of the most recent MIDI output
// enumeration. An error may accompany a still-connected current output.
type MIDIDiscoveryState string

const (
	MIDIDiscoveryDisabled MIDIDiscoveryState = "disabled"
	MIDIDiscoveryOK       MIDIDiscoveryState = "ok"
	MIDIDiscoveryError    MIDIDiscoveryState = "error"
)

// MIDIDevice is one output port in a point-in-time runtime snapshot. Number is
// volatile and must not be treated as a durable device identity.
type MIDIDevice struct {
	Number int    `json:"number"`
	Name   string `json:"name"`
}

// MIDIDevicesDocument describes the cached output enumeration and current
// connection without exposing native-driver failure details.
type MIDIDevicesDocument struct {
	Enabled   bool               `json:"enabled"`
	Discovery MIDIDiscoveryState `json:"discovery"`
	Connected bool               `json:"connected"`
	Current   *MIDIDevice        `json:"current"`
	Devices   []MIDIDevice       `json:"devices"`
}

// MIDIAuditionRequest is a bounded user-facing note trigger. DurationMS is an
// explicit wire unit rather than time.Duration's JSON nanosecond encoding.
type MIDIAuditionRequest struct {
	Channel    int `json:"channel"`
	Note       int `json:"note"`
	Velocity   int `json:"velocity"`
	DurationMS int `json:"duration_ms"`
}

type midiAuditionBody struct {
	Channel    *int `json:"channel"`
	Note       *int `json:"note"`
	Velocity   *int `json:"velocity"`
	DurationMS *int `json:"duration_ms"`
}

func (body *midiAuditionBody) UnmarshalJSON(contents []byte) error {
	if bytes.Equal(bytes.TrimSpace(contents), []byte("null")) {
		return errors.New("MIDI audition request must be an object")
	}
	type plainMIDIAuditionBody midiAuditionBody
	return json.Unmarshal(contents, (*plainMIDIAuditionBody)(body))
}

func (handler *handler) serveMIDIDevices(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		methodNotAllowed(response, request, "GET, HEAD")
		return
	}
	if rejectMIDIQuery(response, request) {
		return
	}
	document, err := handler.backend.MIDIDevices(request.Context())
	if err != nil {
		writeBackendError(response, request, err)
		return
	}
	if !validMIDIDevicesDocument(document) {
		writeProblem(response, request, http.StatusInternalServerError, "internal_error", "backend returned invalid MIDI device state", nil)
		return
	}
	writeJSON(response, request, http.StatusOK, document)
}

func (handler *handler) serveMIDIAudition(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, request, "POST")
		return
	}
	if rejectMIDIQuery(response, request) {
		return
	}
	var body midiAuditionBody
	if !decodeJSONRequestLimit(response, request, &body, maximumMIDIAuditionBodyBytes) {
		return
	}
	audition, fields := validateMIDIAudition(body)
	if len(fields) != 0 {
		writeProblem(
			response,
			request,
			http.StatusUnprocessableEntity,
			"invalid_audition",
			"MIDI audition requires channel 1..16, note 0..127, velocity 1..127, and duration_ms 1..10000",
			fields,
		)
		return
	}
	if err := handler.backend.AuditionMIDI(request.Context(), audition); err != nil {
		writeBackendError(response, request, err)
		return
	}
	writeEmptyResponse(response, http.StatusAccepted)
}

func (handler *handler) serveMIDIPanic(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, request, "POST")
		return
	}
	if rejectMIDIQuery(response, request) || !decodeEmptyMIDIRequest(response, request) {
		return
	}
	if err := handler.backend.PanicMIDI(request.Context()); err != nil {
		writeBackendError(response, request, err)
		return
	}
	writeEmptyResponse(response, http.StatusNoContent)
}

func validateMIDIAudition(body midiAuditionBody) (MIDIAuditionRequest, []string) {
	var request MIDIAuditionRequest
	fields := make([]string, 0, 4)
	if body.Channel == nil || *body.Channel < 1 || *body.Channel > 16 {
		fields = append(fields, "channel")
	} else {
		request.Channel = *body.Channel
	}
	if body.Note == nil || *body.Note < 0 || *body.Note > 127 {
		fields = append(fields, "note")
	} else {
		request.Note = *body.Note
	}
	if body.Velocity == nil || *body.Velocity < 1 || *body.Velocity > 127 {
		fields = append(fields, "velocity")
	} else {
		request.Velocity = *body.Velocity
	}
	if body.DurationMS == nil || *body.DurationMS < 1 || *body.DurationMS > 10_000 {
		fields = append(fields, "duration_ms")
	} else {
		request.DurationMS = *body.DurationMS
	}
	return request, fields
}

func decodeEmptyMIDIRequest(response http.ResponseWriter, request *http.Request) bool {
	contentEncodings := request.Header.Values("Content-Encoding")
	if len(contentEncodings) > 1 || len(contentEncodings) == 1 && !strings.EqualFold(strings.TrimSpace(contentEncodings[0]), "identity") {
		writeProblem(response, request, http.StatusUnsupportedMediaType, "unsupported_content_encoding", "Content-Encoding must be absent or identity", nil)
		return false
	}
	if len(request.Header.Values("Content-Type")) != 0 {
		writeProblem(response, request, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be absent for an empty request", nil)
		return false
	}
	if _, err := io.ReadAll(http.MaxBytesReader(response, request.Body, 0)); err != nil {
		writeProblem(response, request, http.StatusBadRequest, "invalid_body", "request body must be empty", nil)
		return false
	}
	return true
}

func rejectMIDIQuery(response http.ResponseWriter, request *http.Request) bool {
	if request.URL.RawQuery == "" && !request.URL.ForceQuery && request.URL.Fragment == "" {
		return false
	}
	writeProblem(response, request, http.StatusBadRequest, "invalid_query", "MIDI routes do not accept a query string or fragment", nil)
	return true
}

func validMIDIDevicesDocument(document MIDIDevicesDocument) bool {
	if document.Discovery != MIDIDiscoveryDisabled && document.Discovery != MIDIDiscoveryOK && document.Discovery != MIDIDiscoveryError {
		return false
	}
	if document.Enabled == (document.Discovery == MIDIDiscoveryDisabled) {
		return false
	}
	if document.Connected != (document.Current != nil) || !document.Enabled && document.Connected || document.Devices == nil {
		return false
	}
	if document.Current != nil && !validMIDIDevice(*document.Current) {
		return false
	}
	for _, device := range document.Devices {
		if !validMIDIDevice(device) {
			return false
		}
	}
	return true
}

func validMIDIDevice(device MIDIDevice) bool {
	return device.Number >= 0 && utf8.ValidString(device.Name)
}

func writeEmptyResponse(response http.ResponseWriter, status int) {
	if status != http.StatusNoContent {
		response.Header().Set("Content-Length", "0")
	}
	response.WriteHeader(status)
}
