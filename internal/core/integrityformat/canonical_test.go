package integrityformat_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	format "github.com/Adopten123/digital-ruble-deals/internal/core/integrityformat"
	"strings"
	"testing"
	"time"
)

func TestCanonicalJSONTypesNumbersOrderingAndFraming(t *testing.T) {
	for _, pair := range [][2]string{{`{"b":1.00,"a":[true,null,"я"]}`, `{"a":[true,null,"\u044f"],"b":1e0}`}, {`-0.000`, `0`}, {`1.2300e2`, `123`}, {`0.0012300`, `123e-5`}, {`9223372036854775807.000`, `9223372036854775807`}} {
		a, e := format.CanonicalJSON([]byte(pair[0]))
		if e != nil {
			t.Fatal(e)
		}
		b, e := format.CanonicalJSON([]byte(pair[1]))
		if e != nil || !bytes.Equal(a, b) {
			t.Fatalf("not equal: %s / %s: %x %x %v", pair[0], pair[1], a, b, e)
		}
	}
	seen := map[string]string{}
	for _, raw := range []string{`null`, `false`, `true`, `"true"`, `0`, `"0"`, `[]`, `{}`, `["ab","c"]`, `["a","bc"]`, `{"a":null}`, `{"a":""}`} {
		b, e := format.CanonicalJSON([]byte(raw))
		if e != nil {
			t.Fatal(e)
		}
		key := hex.EncodeToString(b)
		if old, ok := seen[key]; ok {
			t.Fatalf("encoding collision: %s / %s", old, raw)
		}
		seen[key] = raw
	}
	b, e := format.CanonicalJSON([]byte(`"abc"`))
	if e != nil || hex.EncodeToString(b) != "730000000000000003616263" {
		t.Fatalf("framed UTF8 vector: %x %v", b, e)
	}
}
func TestCanonicalJSONRejectsMalformedDuplicateTrailingAndHugeInput(t *testing.T) {
	for _, raw := range []string{``, `{"a":1,"a":2}`, `[] true`, `01`, `[1,]`, `1e99999999`, strings.Repeat("[", 258) + strings.Repeat("]", 258), string([]byte{'"', 0xff, '"'})} {
		if _, e := format.CanonicalJSON([]byte(raw)); e == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
func TestEventHashCoversEveryRawFieldAndNormalizesTimezone(t *testing.T) {
	text := func(s string) *string { return &s }
	number := int64(2)
	event := format.Event{ID: "id", DealID: text("deal"), DealVersion: &number, Type: "TYPE", ActorUserID: text("user"), ActorOrganizationID: text("org"), RequestID: "request", Payload: json.RawMessage(`{"b":1,"a":"я"}`), CreatedAt: time.Date(1960, 1, 2, 3, 4, 5, 123456000, time.UTC)}
	original, e := format.EventHash(event)
	if e != nil {
		t.Fatal(e)
	}
	changes := []func(*format.Event){func(v *format.Event) { v.ID = "other" }, func(v *format.Event) { v.DealID = nil }, func(v *format.Event) { v.DealVersion = nil }, func(v *format.Event) { v.Type = "OTHER" }, func(v *format.Event) { v.ActorUserID = nil }, func(v *format.Event) { v.ActorOrganizationID = nil }, func(v *format.Event) { v.ActorSource = text("source") }, func(v *format.Event) { v.RequestID = "other" }, func(v *format.Event) { v.Payload = json.RawMessage(`{"b":2,"a":"я"}`) }, func(v *format.Event) { v.CreatedAt = v.CreatedAt.Add(time.Microsecond) }}
	for i, change := range changes {
		copy := event
		change(&copy)
		got, e := format.EventHash(copy)
		if e != nil || got == original {
			t.Fatalf("field %d not bound: %v", i, e)
		}
	}
	event.CreatedAt = event.CreatedAt.In(time.FixedZone("other", 19800))
	event.Payload = json.RawMessage(`{"a":"\u044f","b":1.000}`)
	same, e := format.EventHash(event)
	if e != nil || same != original {
		t.Fatal("timezone / semantic JSON changed hash", e)
	}
	event.CreatedAt = event.CreatedAt.Add(time.Nanosecond)
	if _, e = format.EventHash(event); e == nil {
		t.Fatal("nanosecond timestamp silently rounded")
	}
}
func TestLinkDomainsBindLogSequencePreviousAndEvent(t *testing.T) {
	genesis := format.Genesis("log")
	event := [32]byte{1}
	link := format.LinkHash("log", 1, genesis, event)
	for _, got := range [][32]byte{format.Genesis("other"), format.LinkHash("other", 1, genesis, event), format.LinkHash("log", 2, genesis, event), format.LinkHash("log", 1, [32]byte{}, event), format.LinkHash("log", 1, genesis, [32]byte{2})} {
		if got == link {
			t.Fatal("link context collision")
		}
	}
}
