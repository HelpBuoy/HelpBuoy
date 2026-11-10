package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/helpbuoy/helpbuoy/proto/control"
	"github.com/helpbuoy/helpbuoy/server/auth"
)

func TestTechieKeys(t *testing.T) {
	st := &State{}
	k1, k2 := newKey(t), newKey(t)
	fp1, fp2 := auth.Fingerprint(k1), auth.Fingerprint(k2)
	if err := st.AddTechie("alice", auth.KeyLine(k1, "laptop")); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTechie("bob", auth.KeyLine(newKey(t), "")); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTechieKey("nobody", auth.KeyLine(k2, "")); err == nil {
		t.Error("added a key to an unknown techie")
	}
	if err := st.AddTechieKey("bob", auth.KeyLine(k1, "")); err == nil {
		t.Error("added alice's key to bob")
	}
	if err := st.AddTechieKey("alice", auth.KeyLine(k2, "desktop")); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTechieKey("alice", auth.KeyLine(k2, "")); err == nil {
		t.Error("added the same key twice")
	}
	if _, _, err := st.AddComputer(auth.KeyLine(k2, "x@y"), "", "bob", time.Now()); err == nil {
		t.Error("registered a techie key as a computer")
	}
	idx, err := NewIndex(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		fp   string
		line string
	}{{fp1, auth.KeyLine(k1, "laptop")}, {fp2, auth.KeyLine(k2, "desktop")}} {
		if id := idx.Lookup(c.fp); id.Role != control.RoleTechie || id.Name != "alice" || id.KeyLine != c.line {
			t.Errorf("lookup %s: %+v", c.fp, id)
		}
	}
	if fps := idx.TechieFingerprints(0); len(fps) != 2 || fps[0] != fp1 || fps[1] != fp2 {
		t.Errorf("fingerprints %v", fps)
	}

	seen := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	st.SetLastSeen(map[string]time.Time{fp2: seen})
	if !st.Techies[0].Keys[1].LastSeen.Equal(seen) || !st.Techies[0].Keys[0].LastSeen.IsZero() {
		t.Errorf("last seen %+v", st.Techies[0].Keys)
	}

	if err := st.RemoveTechieKey("alice", auth.Fingerprint(newKey(t))); err == nil {
		t.Error("removed a key alice doesn't have")
	}
	if err := st.RemoveTechieKey("alice", fp1); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveTechieKey("alice", fp2); err == nil {
		t.Error("removed the last key")
	}
	if len(st.Techies[0].Keys) != 1 || !sameKey(st.Techies[0].Keys[0].PublicKey, fp2) {
		t.Errorf("keys left %+v", st.Techies[0].Keys)
	}

	for range MaxTechieKeys - 1 {
		if err := st.AddTechieKey("alice", auth.KeyLine(newKey(t), "")); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddTechieKey("alice", auth.KeyLine(newKey(t), "")); err == nil {
		t.Errorf("more than %d keys", MaxTechieKeys)
	}
}

// TestTechieKeysJSON: a techie from an older state.json, with only
// public_key, reads as a techie with one key; a techie is written with its
// first key in public_key too, which is all an older version reads.
func TestTechieKeysJSON(t *testing.T) {
	k1, k2 := newKey(t), newKey(t)
	idx, err := Parse([]byte(`{"techies":[{"name":"alice","public_key":"` + auth.KeyLine(k1, "") + `"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	st := idx.State.Clone()
	if len(st.Techies) != 1 || len(st.Techies[0].Keys) != 1 || st.Techies[0].JSONPublicKey != "" {
		t.Fatalf("read %+v", st.Techies)
	}
	if err := st.AddTechieKey("alice", auth.KeyLine(k2, "")); err != nil {
		t.Fatal(err)
	}
	if len(idx.State.Techies[0].Keys) != 1 {
		t.Error("Clone shares the keys of a techie")
	}
	st.SetLastSeen(map[string]time.Time{auth.Fingerprint(k2): time.Now()})
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var old struct {
		Techies []struct {
			Name      string `json:"name"`
			PublicKey string `json:"public_key"`
		} `json:"techies"`
	}
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatal(err)
	}
	if len(old.Techies) != 1 || old.Techies[0].PublicKey != auth.KeyLine(k1, "") {
		t.Errorf("an older version reads %+v from %s", old.Techies, b)
	}
	if !strings.Contains(string(b), `"last_seen"`) {
		t.Errorf("no last_seen in %s", b)
	}
	idx, err = Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if ks := idx.State.Techies[0].Keys; len(ks) != 2 || ks[1].LastSeen.IsZero() {
		t.Errorf("read back %+v", ks)
	}
}

func TestTechieKeysValidation(t *testing.T) {
	k := auth.KeyLine(newKey(t), "")
	for _, s := range []string{
		`{"techies":[{"name":"alice","keys":[]}]}`,
		`{"techies":[{"name":"alice","keys":[{"public_key":"` + k + `"},{"public_key":"` + k + `"}]}]}`,
		`{"techies":[{"name":"alice","keys":[{"public_key":"` + k + `"}]},{"name":"bob","keys":[{"public_key":"` + k + `"}]}]}`,
	} {
		if _, err := Parse([]byte(s)); err == nil {
			t.Errorf("accepted %s", s)
		}
	}
}
