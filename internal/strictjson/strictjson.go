// Package strictjson decodes single-document JSON contracts with the
// project's full hardening: unknown fields rejected, trailing data rejected
// and duplicate object keys rejected at every nesting level — a lenient
// decoder would silently let the last duplicate win.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Decode parses data as exactly one JSON document into v.
func Decode(data []byte, v any) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing data after the JSON document")
	}

	return nil
}

// frame is one open container on the walker's stack; only object frames
// carry a key set, and expectKey alternates with the value tokens inside
// them.
type frame struct {
	keys      map[string]struct{}
	expectKey bool
}

// push opens a container; pop closes the innermost one and restores the
// parent's key/value alternation.
func push(stack []*frame, delim json.Delim) []*frame {
	switch delim {
	case '{':
		return append(stack, &frame{keys: map[string]struct{}{}, expectKey: true})
	case '[':
		return append(stack, &frame{})
	default:
		stack = stack[:len(stack)-1]
		if len(stack) > 0 {
			stack[len(stack)-1].expectKey = stack[len(stack)-1].keys != nil
		}

		return stack
	}
}

// note records one non-delimiter token against the innermost object frame.
func (f *frame) note(token json.Token) error {
	if f == nil || f.keys == nil {
		return nil
	}
	if !f.expectKey {
		f.expectKey = true

		return nil
	}

	key, ok := token.(string)
	if !ok {
		return fmt.Errorf("object key is %T, want string", token)
	}
	if _, dup := f.keys[key]; dup {
		return fmt.Errorf("duplicate object key %q", key)
	}
	f.keys[key] = struct{}{}
	f.expectKey = false

	return nil
}

// rejectDuplicateKeys walks the token stream once, tracking one key set per
// open object so a duplicate at any depth is caught.
func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))

	var stack []*frame
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		if delim, ok := token.(json.Delim); ok {
			stack = push(stack, delim)

			continue
		}
		if len(stack) == 0 {
			continue
		}
		if err := stack[len(stack)-1].note(token); err != nil {
			return err
		}
	}
}
