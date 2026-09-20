package optional

import (
	"encoding/json"
	"testing"
)

// body is a stand-in for a partial update: one optional field, so a test can
// say exactly what the caller sent and read back which of the three states it
// landed in.
type body struct {
	Count Value[int32]  `json:"count"`
	Name  Value[string] `json:"name"`
}

// TestUnmarshalSeparatesAbsentFromNull is the reason the package exists. Both
// states leave no value behind, and a pointer would stop there; IsSet is what
// tells them apart.
func TestUnmarshalSeparatesAbsentFromNull(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantSet   bool
		wantNull  bool
		wantValue int32
		wantOK    bool
	}{
		{name: "absent", raw: `{}`},
		{name: "null", raw: `{"count": null}`, wantSet: true, wantNull: true},
		{name: "zero", raw: `{"count": 0}`, wantSet: true, wantOK: true},
		{name: "value", raw: `{"count": 7}`, wantSet: true, wantValue: 7, wantOK: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got body
			if err := json.Unmarshal([]byte(tc.raw), &got); err != nil {
				t.Fatalf("could not decode %s: %v", tc.raw, err)
			}

			if got.Count.IsSet() != tc.wantSet {
				t.Errorf("IsSet() = %t, want %t", got.Count.IsSet(), tc.wantSet)
			}
			if got.Count.IsNull() != tc.wantNull {
				t.Errorf("IsNull() = %t, want %t", got.Count.IsNull(), tc.wantNull)
			}

			value, ok := got.Count.Get()
			if ok != tc.wantOK {
				t.Errorf("Get() ok = %t, want %t", ok, tc.wantOK)
			}
			if value != tc.wantValue {
				t.Errorf("Get() value = %d, want %d", value, tc.wantValue)
			}
		})
	}
}

// TestZeroValueIsAbsent pins the state a struct arrives in before anything is
// decoded into it. A zero Value that claimed to be set would make every field
// of a partial update look named.
func TestZeroValueIsAbsent(t *testing.T) {
	var v Value[string]

	if v.IsSet() {
		t.Errorf("IsSet() = true, want false for the zero value")
	}
	if v.IsNull() {
		t.Errorf("IsNull() = true, want false for the zero value")
	}
	// Arg is a typed nil rather than an untyped one: the interface itself is
	// non-nil, which is exactly what the driver needs in order to know which
	// SQL type the NULL belongs to.
	p, ok := v.Arg().(*string)
	if !ok || p != nil {
		t.Errorf("Arg() = %v, want a nil *string", v.Arg())
	}
}

// TestArgIsATypedNilForNull is what lets a cleared column need no special case:
// the driver writes a nil *T as SQL NULL, so null and a value travel the same
// path into the query.
func TestArgIsATypedNilForNull(t *testing.T) {
	arg := Null[int32]().Arg()

	p, ok := arg.(*int32)
	if !ok {
		t.Fatalf("Arg() is %T, want *int32", arg)
	}
	if p != nil {
		t.Errorf("Arg() = %d, want a nil pointer", *p)
	}

	set, ok := Of(int32(7)).Arg().(*int32)
	if !ok || set == nil {
		t.Fatalf("Arg() for a set value = %v, want a non-nil *int32", Of(int32(7)).Arg())
	}
	if *set != 7 {
		t.Errorf("Arg() = %d, want 7", *set)
	}
}

// TestUnmarshalRejectsTheWrongType keeps the decoder's own type checking: an
// optional field is optional about presence, not about what it holds.
func TestUnmarshalRejectsTheWrongType(t *testing.T) {
	var got body
	if err := json.Unmarshal([]byte(`{"count": "four"}`), &got); err == nil {
		t.Errorf("decoding a string into Value[int32] succeeded, want an error")
	}
}

// TestMarshalRoundTrip covers the encoding direction, which the API does not
// use today but which a Value reaching a response would depend on.
func TestMarshalRoundTrip(t *testing.T) {
	raw, err := json.Marshal(body{Count: Of(int32(3)), Name: Null[string]()})
	if err != nil {
		t.Fatalf("could not encode: %v", err)
	}

	const want = `{"count":3,"name":null}`
	if string(raw) != want {
		t.Errorf("encoded = %s, want %s", raw, want)
	}
}
