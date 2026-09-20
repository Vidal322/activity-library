// Package optional carries the one distinction a partial update turns on:
// whether a field was named at all.
//
// A PATCH body has three states per field, not two. "min_participants" absent
// means leave the column alone; explicitly null means clear it; a number means
// set it. A *int32 collapses the first two into nil and cannot tell them apart,
// which is why this type exists.
package optional

import (
	"bytes"
	"encoding/json"
)

// Value is a field that may be absent, present and null, or present with a
// value. The zero Value is absent, so a struct of them decoded from a body
// that named nothing changes nothing.
type Value[T any] struct {
	set   bool
	value *T
}

// Of is a Value that carries v, for the caller that has already decided.
func Of[T any](v T) Value[T] {
	return Value[T]{set: true, value: &v}
}

// Null is a Value that was named and explicitly emptied.
func Null[T any]() Value[T] {
	return Value[T]{set: true}
}

func (v Value[T]) IsSet() bool {
	return v.set
}

func (v Value[T]) IsNull() bool {
	return v.set && v.value == nil
}

// Get returns the value and whether there was one. A false means the field was
// absent or null; IsSet separates those.
func (v Value[T]) Get() (T, bool) {
	if v.value == nil {
		var zero T
		return zero, false
	}

	return *v.value, true
}

func (v Value[T]) Arg() any {
	return v.value
}

func (v *Value[T]) UnmarshalJSON(data []byte) error {
	v.set = true

	if bytes.Equal(data, []byte("null")) {
		v.value = nil
		return nil
	}

	var decoded T
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	v.value = &decoded

	return nil
}

func (v Value[T]) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.value)
}
