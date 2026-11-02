package injector

import "testing"

func TestCounter(t *testing.T) {
	var c Counter
	c.Input(1, []byte{1, 2})
	c.Input(1, nil)
	c.Input(4, []byte("x"))
	by, _ := c.Counts()
	if by[1] != 2 || by[4] != 1 {
		t.Fatalf("counts %v", by)
	}
}
