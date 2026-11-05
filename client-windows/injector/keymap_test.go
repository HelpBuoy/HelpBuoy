package injector

import (
	"testing"

	"github.com/helpbuoy/helpbuoy/proto/input"
)

// TestTableCoversInputCodes checks that every key code the techie may send
// (input.Codes) has an entry in the injection table.
func TestTableCoversInputCodes(t *testing.T) {
	for _, c := range input.Codes {
		if _, ok := lookupKey(c); !ok {
			t.Errorf("input code %q has no scan code", c)
		}
	}
}
