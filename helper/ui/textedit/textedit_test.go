package textedit

import "testing"

func check(t *testing.T, e *State, text string, cursor int) {
	t.Helper()
	if e.String() != text || e.Cursor() != cursor {
		t.Fatalf("got %q cursor %d, want %q cursor %d", e.String(), e.Cursor(), text, cursor)
	}
}

func TestTypingAndDeleting(t *testing.T) {
	e := New("")
	e.Insert("helo")
	check(t, e, "helo", 4)
	e.Move(-1, false)
	e.Insert("l")
	check(t, e, "hello", 4)
	e.Backspace(false)
	check(t, e, "helo", 3)
	e.Delete(false)
	check(t, e, "hel", 3)
	e.Delete(false) // at end: nothing
	check(t, e, "hel", 3)
	e.Home(false)
	e.Backspace(false) // at start: nothing
	check(t, e, "hel", 0)
	e.End(false)
	check(t, e, "hel", 3)
}

func TestUnicode(t *testing.T) {
	e := New("añb")
	e.Move(-1, false)
	e.Backspace(false)
	check(t, e, "ab", 1)
	e.Insert("€")
	check(t, e, "a€b", 2)
}

func TestSelection(t *testing.T) {
	e := New("hello world")
	e.Home(false)
	e.Move(1, true)
	e.Move(1, true)
	if s := e.SelectedText(); s != "he" {
		t.Fatalf("selected %q", s)
	}
	e.Insert("J")
	check(t, e, "Jllo world", 1)
	e.SelectAll()
	if !e.HasSelection() || e.SelectedText() != "Jllo world" {
		t.Fatal("select all")
	}
	e.Move(-1, false) // collapses to start
	check(t, e, "Jllo world", 0)
	e.SelectAll()
	e.Move(1, false) // collapses to end
	check(t, e, "Jllo world", 10)
	e.SetCursor(2, false)
	e.SetCursor(6, true)
	if got := e.Cut(); got != "lo w" {
		t.Fatalf("cut %q", got)
	}
	check(t, e, "Jlorld", 2)
	e.SetCursor(5, false)
	e.SetCursor(1, true) // backwards selection
	s, en := e.Selection()
	if s != 1 || en != 5 {
		t.Fatalf("selection %d..%d", s, en)
	}
	e.Backspace(false)
	check(t, e, "Jd", 1)
	e.SelectAll()
	e.Delete(false)
	check(t, e, "", 0)
}

func TestWords(t *testing.T) {
	e := New("ssh-ed25519 AAAA anna@pc")
	e.MoveWord(-1, false)
	check(t, e, e.String(), 22) // before "pc"
	e.MoveWord(-1, false)
	check(t, e, e.String(), 21) // before "@"
	e.Backspace(true)
	check(t, e, "ssh-ed25519 AAAA @pc", 17)
	e.Home(false)
	e.MoveWord(1, false)
	check(t, e, e.String(), 3) // after "ssh"
	e.Delete(true)
	check(t, e, "sshed25519 AAAA @pc", 3)
	e.MoveWord(1, true)
	if e.SelectedText() != "ed25519" {
		t.Fatalf("selected %q", e.SelectedText())
	}
}

func TestPasteKeepsLineBreaks(t *testing.T) {
	e := New("")
	e.Insert("here is my key:\nssh-ed25519 AAAA\nBBBB anna@pc\x00")
	if e.String() != "here is my key:\nssh-ed25519 AAAA\nBBBB anna@pc" {
		t.Fatalf("got %q", e.String())
	}
	d := Display(e.Runes())
	if len([]rune(d)) != e.Len() || d[15] != ' ' {
		t.Fatalf("display %q", d)
	}
}

func TestMax(t *testing.T) {
	e := &State{Max: 5}
	e.Insert("abcdefg")
	check(t, e, "abcde", 5)
	e.Insert("x")
	check(t, e, "abcde", 5)
	e.SelectAll()
	e.Insert("xyz")
	check(t, e, "xyz", 3)
	e.SetText("123456789")
	check(t, e, "12345", 5)
}

func TestIndexAt(t *testing.T) {
	off := []float32{0, 10, 20, 30}
	cases := map[float32]int{-5: 0, 4: 0, 6: 1, 14: 1, 16: 2, 29: 3, 100: 3}
	for x, want := range cases {
		if got := IndexAt(off, x); got != want {
			t.Errorf("IndexAt(%v) = %d, want %d", x, got, want)
		}
	}
	if IndexAt(nil, 3) != 0 {
		t.Error("empty offsets")
	}
}

func TestScrollFor(t *testing.T) {
	if s := ScrollFor(0, 50, 60, 100); s != 0 {
		t.Errorf("visible caret scrolled to %v", s)
	}
	if s := ScrollFor(0, 300, 400, 100); s != 208 {
		t.Errorf("caret past right edge: %v", s)
	}
	if s := ScrollFor(250, 100, 400, 100); s != 92 {
		t.Errorf("caret past left edge: %v", s)
	}
	if s := ScrollFor(500, 400, 400, 100); s != 308 {
		t.Errorf("over-scrolled: %v", s)
	}
}
