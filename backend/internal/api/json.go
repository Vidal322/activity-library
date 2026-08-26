package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const maxBytes = 1 << 20 // 1 MB

func writeJSON(w http.ResponseWriter, status int, data any) error {
	if data == nil || status == http.StatusNoContent {
		w.WriteHeader(status)
		return nil
	}

	var buf bytes.Buffer

	err := json.NewEncoder(&buf).Encode(data)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(status)

	_, err = w.Write(buf.Bytes())
	return err
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.ContentLength > maxBytes {
		return fmt.Errorf("body must not be larger than %d bytes", maxBytes)
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	dec := json.NewDecoder(r.Body)

	// Capture the top-level value as raw bytes first. Decoding straight into
	// dst would accept a bare "null", which is valid JSON that assigns nothing
	// and would leave dst silently zeroed.
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return decodeError(err)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("body must not be null")
	}

	inner := json.NewDecoder(bytes.NewReader(raw))
	inner.DisallowUnknownFields()
	if err := inner.Decode(dst); err != nil {
		return decodeError(err)
	}

	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("body must contain a single JSON value")
	}
	return nil
}

// decodeError turns a decoder error into a message that is safe to show a
// client, hiding Go type and field names.
func decodeError(err error) error {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var maxErr *http.MaxBytesError

	switch {
	case errors.As(err, &syntaxErr):
		return fmt.Errorf("body contains malformed JSON at position %d", syntaxErr.Offset)

	case errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New("body contains malformed JSON")

	case errors.As(err, &typeErr):
		if typeErr.Field != "" {
			return fmt.Errorf("body contains invalid value for field %q", typeErr.Field)
		}
		return fmt.Errorf("body contains invalid value at position %d", typeErr.Offset)

	case errors.Is(err, io.EOF):
		return errors.New("body must not be empty")

	case errors.As(err, &maxErr):
		return fmt.Errorf("body must not be larger than %d bytes", maxBytes)

	// encoding/json exposes no typed error for unknown fields, so matching the
	// message prefix is the only way to detect this.
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		field := strings.TrimPrefix(err.Error(), "json: unknown field ")
		return fmt.Errorf("body contains unknown field %s", field)

	default:
		return err
	}
}

func writeErrorJSON(w http.ResponseWriter, status int, msg string) error {
	return writeJSON(w, status, map[string]string{"error": msg})
}
