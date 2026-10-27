// Package hooks installs and removes hedera's Claude Code hook entries in a
// settings.json without disturbing anything else in it.
package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Value is a JSON value that remembers object key order, so editing a file
// and writing it back changes only what was edited. Numbers keep their
// original spelling.
type Value struct {
	// Exactly one of these describes the value.
	Obj    *Object
	Arr    []*Value
	Scalar any // string, json.Number, bool, or nil for null
	IsArr  bool
}

// Object is an ordered JSON object. Duplicate keys are kept as they are.
type Object struct {
	Keys []string
	Vals []*Value
}

func (o *Object) Get(key string) *Value {
	for i, k := range o.Keys {
		if k == key {
			return o.Vals[i]
		}
	}
	return nil
}

// Set replaces the first value for key, or appends it.
func (o *Object) Set(key string, v *Value) {
	for i, k := range o.Keys {
		if k == key {
			o.Vals[i] = v
			return
		}
	}
	o.Keys = append(o.Keys, key)
	o.Vals = append(o.Vals, v)
}

// Delete removes every value for key.
func (o *Object) Delete(key string) {
	keys, vals := o.Keys[:0], o.Vals[:0]
	for i, k := range o.Keys {
		if k != key {
			keys = append(keys, k)
			vals = append(vals, o.Vals[i])
		}
	}
	o.Keys, o.Vals = keys, vals
}

func NewObject() *Value           { return &Value{Obj: &Object{}} }
func NewArray(v ...*Value) *Value { return &Value{IsArr: true, Arr: v} }
func String(s string) *Value      { return &Value{Scalar: s} }
func Bool(b bool) *Value          { return &Value{Scalar: b} }
func Number(n int) *Value         { return &Value{Scalar: json.Number(fmt.Sprint(n))} }

// Str returns the value as a string, if it is one.
func (v *Value) Str() (string, bool) {
	if v == nil {
		return "", false
	}
	s, ok := v.Scalar.(string)
	return s, ok
}

// Parse decodes one JSON document.
func Parse(data []byte) (*Value, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after the JSON document")
	}
	return v, nil
}

func parseValue(dec *json.Decoder) (*Value, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := &Object{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, errors.New("object key is not a string")
				}
				val, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				obj.Keys = append(obj.Keys, key)
				obj.Vals = append(obj.Vals, val)
			}
			if _, err := dec.Token(); err != nil { // '}'
				return nil, err
			}
			return &Value{Obj: obj}, nil
		case '[':
			arr := &Value{IsArr: true}
			for dec.More() {
				val, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				arr.Arr = append(arr.Arr, val)
			}
			if _, err := dec.Token(); err != nil { // ']'
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return &Value{Scalar: t}, nil
	}
}

// Marshal encodes v with two-space indentation, the way Claude Code writes
// its settings files.
func Marshal(v *Value) []byte {
	var b bytes.Buffer
	write(&b, v, 0)
	b.WriteByte('\n')
	return b.Bytes()
}

func write(b *bytes.Buffer, v *Value, depth int) {
	indent := func(d int) { b.WriteString(strings.Repeat("  ", d)) }
	switch {
	case v.Obj != nil:
		if len(v.Obj.Keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range v.Obj.Keys {
			indent(depth + 1)
			writeScalar(b, k)
			b.WriteString(": ")
			write(b, v.Obj.Vals[i], depth+1)
			if i < len(v.Obj.Keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		indent(depth)
		b.WriteByte('}')
	case v.IsArr:
		if len(v.Arr) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, item := range v.Arr {
			indent(depth + 1)
			write(b, item, depth+1)
			if i < len(v.Arr)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		indent(depth)
		b.WriteByte(']')
	default:
		writeScalar(b, v.Scalar)
	}
}

func writeScalar(b *bytes.Buffer, s any) {
	if n, ok := s.(json.Number); ok {
		b.WriteString(n.String())
		return
	}
	var tmp bytes.Buffer
	enc := json.NewEncoder(&tmp)
	enc.SetEscapeHTML(false) // keep "<", ">" and "&" as written
	enc.Encode(s)
	b.Write(bytes.TrimRight(tmp.Bytes(), "\n"))
}
