package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Vidal322/activity-library/internal/store"
)

// parseIDParam validates each repeated value of one query parameter as a UUID
// and drops duplicates, which the store's AND match cannot tolerate.
func parseIDParam(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}

	ids := make([]string, 0, len(values))
	seen := make(map[[16]byte]struct{}, len(values))

	for _, v := range values {
		var parsed pgtype.UUID
		if err := parsed.Scan(v); err != nil {
			return nil, fmt.Errorf("%q is not a valid id", v)
		}
		if _, dup := seen[parsed.Bytes]; dup {
			continue
		}
		seen[parsed.Bytes] = struct{}{}
		ids = append(ids, v)
	}

	return ids, nil
}

// singleValue pulls the one value a scalar filter admits. Repeats are rejected
// rather than resolved
func singleValue(values []string) (string, bool, error) {
	switch len(values) {
	case 0:
		return "", false, nil
	case 1:
		return values[0], true, nil
	default:
		return "", false, errors.New("expects a single value")
	}
}

// parseIntParam reads one scalar filter, nil being the absent one. Zero and
// negatives are refused along with the unparseable
func parseIntParam(values []string) (*int32, error) {
	raw, present, err := singleValue(values)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}

	// bitSize 32 makes anything the integer column could not hold an error
	// here rather than a wrapped value further down.
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("%q is not a positive whole number", raw)
	}

	parsed := int32(n)
	return &parsed, nil
}

// The cursor is opaque to the client: it is handed back the value the previous
// response carried, and never assembles one.
const cursorSeparator = "|"

func encodeCursor(c store.GameCursor) string {
	raw := c.CreatedAt.UTC().Format(time.RFC3339Nano) + cursorSeparator + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// parseCursorParam reads the page marker, nil being the first page.
func parseCursorParam(values []string) (*store.GameCursor, error) {
	raw, present, err := singleValue(values)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}

	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("is not a cursor this endpoint issued")
	}

	createdAt, id, found := strings.Cut(string(decoded), cursorSeparator)
	if !found {
		return nil, errors.New("is not a cursor this endpoint issued")
	}

	at, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, errors.New("is not a cursor this endpoint issued")
	}

	var parsed pgtype.UUID
	if err := parsed.Scan(id); err != nil {
		return nil, errors.New("is not a cursor this endpoint issued")
	}

	return &store.GameCursor{CreatedAt: at, ID: id}, nil
}

// parseLimitParam reads the page size, zero standing for the default the store
// applies. A limit past the ceiling is refused
func parseLimitParam(values []string) (int32, error) {
	raw, present, err := singleValue(values)
	if err != nil {
		return 0, err
	}
	if !present {
		return 0, nil
	}

	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a positive whole number", raw)
	}
	if int32(n) > store.MaxGameLimit {
		return 0, fmt.Errorf("must not exceed %d", store.MaxGameLimit)
	}

	return int32(n), nil
}

// parseBoolParam reads one boolean filter. Absent is nil and false is a filter
// in its own right, so the three states stay apart.
func parseBoolParam(values []string) (*bool, error) {
	raw, present, err := singleValue(values)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}

	switch raw {
	case "true":
		parsed := true
		return &parsed, nil
	case "false":
		parsed := false
		return &parsed, nil
	default:
		return nil, fmt.Errorf("%q is not true or false", raw)
	}
}

// parseTextParam reads the free-text search, nil being no search at all. A
// value of nothing but spaces is refused rather than read as absent
func parseTextParam(values []string) (*string, error) {
	raw, present, err := singleValue(values)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}

	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("must not be empty")
	}

	return &trimmed, nil
}

// A ranked search has no keyset to bookmark, so its cursor carries the row
// count the next page starts at instead. The prefix keeps the two kinds of
// cursor apart, so one handed to the other path is refused outright rather
// than quietly read as a position it does not describe.
const offsetCursorPrefix = "o"

func encodeOffsetCursor(offset int32) string {
	raw := offsetCursorPrefix + cursorSeparator + strconv.FormatInt(int64(offset), 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// parseOffsetCursor reads the search page marker, zero being the first page.
// The ceiling is the store's: past it the database would rank and sort the
// whole matching set only to throw away everything before the offset.
func parseOffsetCursor(values []string) (int32, error) {
	raw, present, err := singleValue(values)
	if err != nil {
		return 0, err
	}
	if !present {
		return 0, nil
	}

	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, errors.New("is not a cursor this endpoint issued")
	}

	prefix, count, found := strings.Cut(string(decoded), cursorSeparator)
	if !found || prefix != offsetCursorPrefix {
		return 0, errors.New("is not a cursor this endpoint issued")
	}

	n, err := strconv.ParseInt(count, 10, 32)
	if err != nil || n < 0 {
		return 0, errors.New("is not a cursor this endpoint issued")
	}
	if int32(n) > store.MaxGameSearchOffset {
		return 0, fmt.Errorf("must not reach past result %d", store.MaxGameSearchOffset)
	}

	return int32(n), nil
}
