package managementapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
)

const (
	defaultFlowPageLimit = 500
	maximumFlowPageLimit = 5000
	maximumJSONDepth     = 100
)

// FlowPageRequest is a normalized bounded window into the live registry.
type FlowPageRequest struct {
	Limit int
}

// FlowEndpoint is one side of a canonical bidirectional flow.
type FlowEndpoint struct {
	Address string `json:"address"`
	Port    uint16 `json:"port"`
}

// FlowSnapshot is the stable transport view of one retained flow.
type FlowSnapshot struct {
	ID                string       `json:"id"`
	Protocol          string       `json:"protocol"`
	EndpointA         FlowEndpoint `json:"endpoint_a"`
	EndpointB         FlowEndpoint `json:"endpoint_b"`
	LatestSource      FlowEndpoint `json:"latest_source"`
	LatestDestination FlowEndpoint `json:"latest_destination"`
	FirstSeen         time.Time    `json:"first_seen"`
	LastSeen          time.Time    `json:"last_seen"`
	Packets           uint64       `json:"packets"`
	Bytes             uint64       `json:"bytes"`
	PacketsAToB       uint64       `json:"packets_a_to_b"`
	PacketsBToA       uint64       `json:"packets_b_to_a"`
	Muted             bool         `json:"muted"`
	Soloed            bool         `json:"soloed"`
	State             string       `json:"state"`
	Channel           uint8        `json:"channel"`
	RuleID            string       `json:"rule_id,omitempty"`
	RuleTier          string       `json:"rule_tier"`
	RuleName          string       `json:"rule_name,omitempty"`
	DecisionReason    string       `json:"decision_reason"`
	MatchedPredicates []string     `json:"matched_predicates"`
	Mode              string       `json:"mode"`
	Root              uint8        `json:"root"`
	FixedIdentity     bool         `json:"fixed_identity"`
}

// FlowOverlay is the complete temporary, non-persisted mute and solo state.
type FlowOverlay struct {
	Muted  []string `json:"muted"`
	Soloed []string `json:"soloed"`
}

// FlowPage is ordered newest-first and bounded by FlowPageRequest.
type FlowPage struct {
	Flows     []FlowSnapshot `json:"flows"`
	Overlay   FlowOverlay    `json:"overlay"`
	Total     int            `json:"total"`
	Limit     int            `json:"limit"`
	Truncated bool           `json:"truncated"`
}

type flowSetRequest struct {
	FlowIDs []string `json:"flow_ids"`
}

// UnmarshalJSON enforces exact schema-key spelling. encoding/json otherwise
// matches struct field names case-insensitively, which would allow FLOW_IDS to
// bypass the duplicate-name scan as a distinct name and then overwrite
// flow_ids during struct decoding.
func (request *flowSetRequest) UnmarshalJSON(contents []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(contents, &object); err != nil {
		return err
	}
	for name := range object {
		if name != "flow_ids" {
			return errors.New("flow overlay object contains an unknown field")
		}
	}
	raw, exists := object["flow_ids"]
	if !exists {
		return nil
	}
	return json.Unmarshal(raw, &request.FlowIDs)
}

func (handler *handler) flows(response http.ResponseWriter, request *http.Request) {
	pageRequest, err := parseFlowPageRequest(request)
	if err != nil {
		writeProblem(response, request, http.StatusBadRequest, "invalid_query", err.Error(), nil)
		return
	}
	page, err := handler.backend.Flows(request.Context(), pageRequest)
	if err != nil {
		writeBackendError(response, request, err)
		return
	}
	writeJSON(response, request, http.StatusOK, page)
}

func (handler *handler) setMutedFlows(response http.ResponseWriter, request *http.Request) {
	handler.setFlowOverlay(response, request, handler.backend.SetMutedFlows)
}

func (handler *handler) setSoloedFlows(response http.ResponseWriter, request *http.Request) {
	handler.setFlowOverlay(response, request, handler.backend.SetSoloedFlows)
}

func (handler *handler) setFlowOverlay(
	response http.ResponseWriter,
	request *http.Request,
	set func(context.Context, []string) (FlowOverlay, error),
) {
	var body flowSetRequest
	if !decodeJSONRequest(response, request, &body) {
		return
	}
	if body.FlowIDs == nil {
		writeProblem(response, request, http.StatusUnprocessableEntity, "invalid_flow_set", "flow_ids must be an array", nil)
		return
	}
	if _, found := duplicateFlowID(body.FlowIDs); found {
		writeProblem(response, request, http.StatusUnprocessableEntity, "invalid_flow_set", "flow_ids must contain unique values", nil)
		return
	}
	overlay, err := set(request.Context(), body.FlowIDs)
	if err != nil {
		writeBackendError(response, request, err)
		return
	}
	writeJSON(response, request, http.StatusOK, overlay)
}

func parseFlowPageRequest(request *http.Request) (FlowPageRequest, error) {
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return FlowPageRequest{}, errors.New("query string is malformed")
	}
	for key := range query {
		if key != "limit" {
			return FlowPageRequest{}, errors.New("query may contain only the limit parameter")
		}
	}
	limit, err := parseSingleNonNegativeQuery(query["limit"], "limit", defaultFlowPageLimit)
	if err != nil {
		return FlowPageRequest{}, err
	}
	if limit == 0 || limit > maximumFlowPageLimit {
		return FlowPageRequest{}, fmt.Errorf("limit must be between 1 and %d", maximumFlowPageLimit)
	}
	return FlowPageRequest{Limit: limit}, nil
}

func duplicateFlowID(values []string) (string, bool) {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return value, true
		}
		seen[value] = struct{}{}
	}
	return "", false
}

func parseSingleNonNegativeQuery(values []string, name string, defaultValue int) (int, error) {
	if len(values) == 0 {
		return defaultValue, nil
	}
	if len(values) != 1 || values[0] == "" {
		return 0, fmt.Errorf("%s must appear once as a non-negative integer", name)
	}
	value, err := strconv.ParseUint(values[0], 10, 31)
	if err != nil {
		return 0, fmt.Errorf("%s must appear once as a non-negative integer", name)
	}
	return int(value), nil
}

func decodeJSONRequest(response http.ResponseWriter, request *http.Request, target any) bool {
	return decodeJSONRequestLimit(response, request, target, config.MaximumBytes)
}

func decodeJSONRequestLimit(response http.ResponseWriter, request *http.Request, target any, maximumBytes int64) bool {
	contentEncodings := request.Header.Values("Content-Encoding")
	if len(contentEncodings) > 1 || len(contentEncodings) == 1 && !strings.EqualFold(strings.TrimSpace(contentEncodings[0]), "identity") {
		writeProblem(response, request, http.StatusUnsupportedMediaType, "unsupported_content_encoding", "Content-Encoding must be absent or identity", nil)
		return false
	}
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		writeProblem(response, request, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json", nil)
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(contentTypes[0])
	validParameters := len(parameters) == 0 || len(parameters) == 1 && strings.EqualFold(parameters["charset"], "utf-8")
	if err != nil || !strings.EqualFold(mediaType, "application/json") || !validParameters {
		writeProblem(response, request, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json", nil)
		return false
	}

	contents, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maximumBytes))
	if err != nil {
		var maximumBytesError *http.MaxBytesError
		if errors.As(err, &maximumBytesError) {
			writeProblem(response, request, http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("request body exceeds %d bytes", maximumBytes), nil)
			return false
		}
		writeProblem(response, request, http.StatusBadRequest, "invalid_body", "could not read the request body", nil)
		return false
	}
	if !utf8.Valid(contents) {
		writeProblem(response, request, http.StatusBadRequest, "invalid_body", "request body must contain valid UTF-8 JSON", nil)
		return false
	}
	if err := rejectDuplicateJSONNames(contents); err != nil {
		writeProblem(response, request, http.StatusBadRequest, "invalid_body", "request body must contain one JSON value with unique object names", nil)
		return false
	}
	if err := rejectJSONNamesOutsideSchema(contents, target); err != nil {
		writeProblem(response, request, http.StatusBadRequest, "invalid_body", "request body must contain one valid JSON value matching the schema", nil)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeProblem(response, request, http.StatusBadRequest, "invalid_body", "request body must contain one valid JSON value matching the schema", nil)
		return false
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeProblem(response, request, http.StatusBadRequest, "invalid_body", "request body must contain exactly one JSON value", nil)
		return false
	}
	return true
}

func rejectDuplicateJSONNames(contents []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	if err := scanJSONValue(decoder, 0); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func rejectJSONNamesOutsideSchema(contents []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	return validateJSONNames(value, reflect.TypeOf(target))
}

func validateJSONNames(value any, targetType reflect.Type) error {
	for targetType != nil && targetType.Kind() == reflect.Pointer {
		targetType = targetType.Elem()
	}
	if targetType == nil || value == nil {
		return nil
	}

	switch targetType.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		fields := jsonFields(targetType)
		for name, child := range object {
			fieldType, exists := fields[name]
			if !exists {
				return fmt.Errorf("unknown JSON object name %q", name)
			}
			if err := validateJSONNames(child, fieldType); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		array, ok := value.([]any)
		if !ok {
			return nil
		}
		for _, child := range array {
			if err := validateJSONNames(child, targetType.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok || targetType.Key().Kind() != reflect.String {
			return nil
		}
		for _, child := range object {
			if err := validateJSONNames(child, targetType.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func jsonFields(targetType reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, targetType.NumField())
	for index := 0; index < targetType.NumField(); index++ {
		field := targetType.Field(index)
		if field.PkgPath != "" {
			continue
		}
		name := field.Name
		if tag, exists := field.Tag.Lookup("json"); exists {
			name, _, _ = strings.Cut(tag, ",")
		}
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	return fields
}

func scanJSONValue(decoder *json.Decoder, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	if depth >= maximumJSONDepth {
		return errors.New("JSON nesting is too deep")
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("JSON object name is not a string")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("duplicate JSON object name %q", name)
			}
			seen[name] = struct{}{}
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("additional JSON value")
		}
		return err
	}
	return nil
}
