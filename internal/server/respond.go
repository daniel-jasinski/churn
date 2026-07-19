package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"churn/internal/domain"
	"churn/internal/event"
)

// apiError is one non-2xx response: the HTTP status plus the §M5 structured
// envelope fields. Domain rejections map kind and ids verbatim; server-level
// failures use the server kinds documented in the package comment.
type apiError struct {
	status  int
	kind    string
	message string
	ids     []string
	details map[string]any
}

func (e *apiError) Error() string { return fmt.Sprintf("%s: %s", e.kind, e.message) }

// errorBody is the wire form of the envelope.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Kind    string         `json:"kind"`
	Message string         `json:"message"`
	IDs     []string       `json:"ids,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// writeJSON writes v as the JSON response body with the given status.
func writeJSON(rw http.ResponseWriter, status int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	enc := json.NewEncoder(rw)
	if err := enc.Encode(v); err != nil {
		// Headers are gone; nothing to do but note it via the body cutoff.
		return
	}
}

// writeError writes the structured error envelope.
func writeError(rw http.ResponseWriter, e *apiError) {
	writeJSON(rw, e.status, errorBody{Error: errorDetail{
		Kind: e.kind, Message: e.message, IDs: e.ids, Details: e.details,
	}})
}

// errNotFound is the 404 for ids that do not resolve in the projection.
func errNotFound(id string) *apiError {
	return &apiError{status: http.StatusNotFound, kind: "not_found",
		message: fmt.Sprintf("%s does not exist", id), ids: []string{id}}
}

// errBadRequest is the 400 for malformed requests.
func errBadRequest(format string, args ...any) *apiError {
	return &apiError{status: http.StatusBadRequest, kind: "bad_request",
		message: fmt.Sprintf(format, args...)}
}

// domainStatus maps a domain.Error kind to its HTTP status — the table in
// the package comment. Conflicts with the current state of the world are
// 409; rejections of the request's own content are 422; a missing target is
// 404. Unknown kinds (future domain kinds) default to 422, the safest
// "your request was understood but refused" reading.
func domainStatus(kind string) int {
	switch kind {
	case domain.KindUnknownEntity:
		return http.StatusNotFound
	case domain.KindStaleVersion, domain.KindCycle, domain.KindRetractionBlocked,
		domain.KindSemanticImmutable, domain.KindCapacity, domain.KindInfeasible:
		return http.StatusConflict
	default:
		return http.StatusUnprocessableEntity
	}
}

// mapError renders any error from the submit path as an apiError:
// domain.Error passes kind/message/ids through with the documented status;
// everything else is an internal 500 (handlers validate request content
// before submitting, so client-caused failures never reach here as plain
// errors).
func mapError(err error) *apiError {
	var de *domain.Error
	if errors.As(err, &de) {
		return &apiError{status: domainStatus(de.Kind), kind: de.Kind, message: de.Message, ids: de.IDs}
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	return &apiError{status: http.StatusInternalServerError, kind: "internal", message: err.Error()}
}

// decodeJSON decodes a mutation request body into dst: the Content-Type must
// be application/json (415 otherwise), the body must be a single JSON value
// with no unknown fields (400), and the middleware's 4 MiB cap surfaces as
// 413. Strict decoding is what makes "PATCH is full replacement" mechanical:
// a true patch either omits required fields (rejected by payload validation)
// or invents unknown ones (rejected here).
func decodeJSON(r *http.Request, dst any) *apiError {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return &apiError{status: http.StatusUnsupportedMediaType, kind: "unsupported_media_type",
			message: "mutations require Content-Type: application/json"}
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil || mt != "application/json" {
		return &apiError{status: http.StatusUnsupportedMediaType, kind: "unsupported_media_type",
			message: fmt.Sprintf("unsupported content type %q: mutations take application/json", ct)}
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return &apiError{status: http.StatusRequestEntityTooLarge, kind: "payload_too_large",
				message: fmt.Sprintf("request body exceeds %d bytes", mbe.Limit)}
		}
		return errBadRequest("decoding request body: %v", err)
	}
	if dec.More() {
		return errBadRequest("request body must be a single JSON value")
	}
	return nil
}

// expectedFromIfMatch reads the optional If-Match header as the expected
// version (§5.2) of the entity a single-entity mutation writes: the version
// integer, optionally quoted. Returns a nil map when the header is absent.
func expectedFromIfMatch(r *http.Request, id string) (map[string]int64, *apiError) {
	h := strings.TrimSpace(r.Header.Get("If-Match"))
	if h == "" {
		return nil, nil
	}
	v, err := strconv.ParseInt(strings.Trim(h, `"`), 10, 64)
	if err != nil {
		return nil, errBadRequest("If-Match %q: want the entity version integer", h)
	}
	return map[string]int64{id: v}, nil
}

// validatePayload runs the event payload's own shape validation, mapping a
// failure to 400 — client-caused, and caught before the writer is involved.
func validatePayload(p event.Payload) *apiError {
	if err := p.Validate(); err != nil {
		return errBadRequest("invalid payload: %v", err)
	}
	return nil
}
