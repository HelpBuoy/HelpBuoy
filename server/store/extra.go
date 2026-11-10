package store

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
)

// Extra holds the fields of a state.json object that this version does not
// know, so that saving the state writes them back unchanged. A newer desk app
// or helpbuoy-server sharing the state directory can then add fields without
// an older one dropping them.
type Extra map[string]json.RawMessage

type (
	plainState     State
	plainTechie    Techie
	plainTechieKey TechieKey
	plainComputer  Computer
	plainCustomer  Customer
)

func (st *State) UnmarshalJSON(b []byte) error {
	var err error
	st.Extra, err = unmarshalWithExtra(b, (*plainState)(st))
	return err
}

func (st State) MarshalJSON() ([]byte, error) {
	return marshalWithExtra(plainState(st), st.Extra)
}

func (cu *Customer) UnmarshalJSON(b []byte) error {
	var err error
	cu.Extra, err = unmarshalWithExtra(b, (*plainCustomer)(cu))
	return err
}

func (cu Customer) MarshalJSON() ([]byte, error) {
	return marshalWithExtra(plainCustomer(cu), cu.Extra)
}

// UnmarshalJSON reads a techie. One written by a version with one key per
// techie only has public_key, which becomes its only key.
func (h *Techie) UnmarshalJSON(b []byte) error {
	var err error
	h.Extra, err = unmarshalWithExtra(b, (*plainTechie)(h))
	if err != nil {
		return err
	}
	if len(h.Keys) == 0 && h.JSONPublicKey != "" {
		h.Keys = []TechieKey{{PublicKey: h.JSONPublicKey}}
	}
	h.JSONPublicKey = ""
	return nil
}

// MarshalJSON writes a techie, with its first key also in public_key, so an
// older version that knows one key per techie still lets it in with that key.
func (h Techie) MarshalJSON() ([]byte, error) {
	h.JSONPublicKey = ""
	if len(h.Keys) > 0 {
		h.JSONPublicKey = h.Keys[0].PublicKey
	}
	return marshalWithExtra(plainTechie(h), h.Extra)
}

func (hk *TechieKey) UnmarshalJSON(b []byte) error {
	var err error
	hk.Extra, err = unmarshalWithExtra(b, (*plainTechieKey)(hk))
	return err
}

func (hk TechieKey) MarshalJSON() ([]byte, error) {
	return marshalWithExtra(plainTechieKey(hk), hk.Extra)
}

func (c *Computer) UnmarshalJSON(b []byte) error {
	var err error
	c.Extra, err = unmarshalWithExtra(b, (*plainComputer)(c))
	return err
}

func (c Computer) MarshalJSON() ([]byte, error) {
	return marshalWithExtra(plainComputer(c), c.Extra)
}

// unmarshalWithExtra decodes b into v, a pointer to a struct, and returns the
// fields of b that v has no field for.
func unmarshalWithExtra(b []byte, v any) (Extra, error) {
	if err := json.Unmarshal(b, v); err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	known := knownFields(reflect.TypeOf(v).Elem())
	var extra Extra
	for k, raw := range all {
		// encoding/json matches field names without regard to case.
		if known[strings.ToLower(k)] {
			continue
		}
		if extra == nil {
			extra = Extra{}
		}
		var c bytes.Buffer
		if err := json.Compact(&c, raw); err != nil {
			return nil, err
		}
		extra[k] = c.Bytes()
	}
	return extra, nil
}

// marshalWithExtra encodes v, a struct, followed by the extra fields in
// sorted order.
func marshalWithExtra(v any, extra Extra) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return b, err
	}
	var buf bytes.Buffer
	buf.Write(b[:len(b)-1]) // without the closing brace
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	empty := len(b) == 2
	for _, k := range keys {
		if !empty {
			buf.WriteByte(',')
		}
		empty = false
		name, _ := json.Marshal(k)
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(extra[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

var knownCache sync.Map // reflect.Type -> map[string]bool

// knownFields returns the lower-cased JSON names of the fields of struct type t.
func knownFields(t reflect.Type) map[string]bool {
	if m, ok := knownCache.Load(t); ok {
		return m.(map[string]bool)
	}
	m := map[string]bool{}
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		m[strings.ToLower(name)] = true
	}
	knownCache.Store(t, m)
	return m
}
