// Package integrityformat defines the versioned byte format shared by PostgreSQL
// append triggers and the independent Go verifier. It never hashes float values.
package integrityformat

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const Version = 1
const FormatVersion = Version
const eventDomain = "poaktu:audit:event:v1\x00"
const genesisDomain = "poaktu:audit:genesis:v1\x00"
const linkDomain = "poaktu:audit:link:v1\x00"
const maxJSONDepth = 256

// Event is the complete stored audit fact, not the public safe projection.
type Event struct {
	ID                  string
	DealID              *string
	DealVersion         *int64
	Type                string
	ActorUserID         *string
	ActorOrganizationID *string
	ActorSource         *string
	RequestID           string
	Payload             json.RawMessage
	CreatedAt           time.Time
}

func frame(text string) []byte {
	out := make([]byte, 8+len(text))
	binary.BigEndian.PutUint64(out, uint64(len(text)))
	copy(out[8:], text)
	return out
}

// CanonicalJSON uses n/f/t tags, framed UTF-8 s/d values, and counted a/o
// containers. Object keys sort by UTF-8 bytes; JSON numbers remain exact decimal.
func CanonicalJSON(raw []byte) ([]byte, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("JSON is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	out, err := jsonValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, errors.New("JSON has trailing data")
	}
	return out, nil
}
func jsonValue(decoder *json.Decoder, depth int) ([]byte, error) {
	if depth > maxJSONDepth {
		return nil, errors.New("JSON nesting exceeds integrity format")
	}
	value, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch v := value.(type) {
	case nil:
		return []byte{'n'}, nil
	case bool:
		if v {
			return []byte{'t'}, nil
		}
		return []byte{'f'}, nil
	case string:
		return append([]byte{'s'}, frame(v)...), nil
	case json.Number:
		normalized, err := normalizeNumber(string(v))
		if err != nil {
			return nil, err
		}
		return append([]byte{'d'}, frame(normalized)...), nil
	case json.Delim:
		switch v {
		case '[':
			var children [][]byte
			for decoder.More() {
				child, e := jsonValue(decoder, depth+1)
				if e != nil {
					return nil, e
				}
				children = append(children, child)
			}
			if end, e := decoder.Token(); e != nil || end != json.Delim(']') {
				return nil, errors.New("unterminated JSON array")
			}
			out := append([]byte{'a'}, count(len(children))...)
			for _, child := range children {
				out = append(out, child...)
			}
			return out, nil
		case '{':
			children := map[string][]byte{}
			for decoder.More() {
				key, e := decoder.Token()
				if e != nil {
					return nil, e
				}
				name, ok := key.(string)
				if !ok {
					return nil, errors.New("invalid JSON object key")
				}
				if _, exists := children[name]; exists {
					return nil, errors.New("duplicate JSON object key")
				}
				child, e := jsonValue(decoder, depth+1)
				if e != nil {
					return nil, e
				}
				children[name] = child
			}
			if end, e := decoder.Token(); e != nil || end != json.Delim('}') {
				return nil, errors.New("unterminated JSON object")
			}
			keys := make([]string, 0, len(children))
			for key := range children {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			out := append([]byte{'o'}, count(len(keys))...)
			for _, key := range keys {
				out = append(out, 's')
				out = append(out, frame(key)...)
				out = append(out, children[key]...)
			}
			return out, nil
		}
	}
	return nil, errors.New("invalid JSON token")
}
func count(n int) []byte { var b [8]byte; binary.BigEndian.PutUint64(b[:], uint64(n)); return b[:] }

// PostgreSQL numeric JSON values normalize to non-exponent decimal, without
// insignificant zeroes. Bound expansion above PostgreSQL numeric's finite range.
func normalizeNumber(value string) (string, error) {
	negative := strings.HasPrefix(value, "-")
	if negative {
		value = value[1:]
	}
	exponent := 0
	if at := strings.IndexAny(value, "eE"); at >= 0 {
		parsed, e := strconv.ParseInt(value[at+1:], 10, 32)
		if e != nil || parsed < -200000 || parsed > 200000 {
			return "", errors.New("JSON decimal exponent exceeds integrity format")
		}
		exponent = int(parsed)
		value = value[:at]
	}
	fraction := 0
	if at := strings.IndexByte(value, '.'); at >= 0 {
		fraction = len(value) - at - 1
		value = value[:at] + value[at+1:]
	}
	value = strings.TrimLeft(value, "0")
	if value == "" {
		return "0", nil
	}
	scale := fraction - exponent
	// Remove fractional zeroes before rendering; integers retain their zeroes.
	for scale > 0 && strings.HasSuffix(value, "0") {
		value = value[:len(value)-1]
		scale--
	}
	if len(value)+max(0, -scale) > 200000 || scale > 200000 {
		return "", errors.New("JSON decimal exceeds integrity format")
	}
	var normalized string
	switch {
	case scale <= 0:
		normalized = value + strings.Repeat("0", -scale)
	case scale >= len(value):
		normalized = "0." + strings.Repeat("0", scale-len(value)) + value
	default:
		normalized = value[:len(value)-scale] + "." + value[len(value)-scale:]
	}
	if negative {
		normalized = "-" + normalized
	}
	return normalized, nil
}

func EventHash(event Event) ([32]byte, error) {
	stamp := event.CreatedAt.UTC()
	if stamp.Year() < 1 || stamp.Year() > 9999 || stamp.Nanosecond()%1000 != 0 {
		return [32]byte{}, errors.New("audit time must be finite AD UTC-compatible microseconds")
	}
	// RawMessage keeps every decimal exact; the common encoder handles nullable fields.
	raw, err := json.Marshal([]any{event.ID, event.DealID, event.DealVersion, event.Type, event.ActorUserID, event.ActorOrganizationID, event.ActorSource, event.RequestID, event.Payload, stamp.Format("2006-01-02T15:04:05.000000Z")})
	if err != nil {
		return [32]byte{}, fmt.Errorf("audit event encoding: %w", err)
	}
	canonical, err := CanonicalJSON(raw)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(append([]byte(eventDomain), canonical...)), nil
}
func Genesis(logID string) [32]byte {
	return sha256.Sum256(append([]byte(genesisDomain), frame(logID)...))
}
func LinkHash(logID string, sequence int64, previous, event [32]byte) [32]byte {
	raw := append([]byte(linkDomain), frame(logID)...)
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(sequence))
	raw = append(raw, number[:]...)
	raw = append(raw, previous[:]...)
	raw = append(raw, event[:]...)
	return sha256.Sum256(raw)
}
