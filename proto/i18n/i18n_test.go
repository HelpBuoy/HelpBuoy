package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mevdschee/helpbuoy/proto/control"
)

func TestFill(t *testing.T) {
	for _, c := range []struct {
		in   string
		args []any
		want string
	}{
		{"Connect", nil, "Connect"},
		{"Allow {0} ({1})?", []any{"ann", "SHA256:x"}, "Allow ann (SHA256:x)?"},
		{"{1} then {0} and {1}", []any{"a", "b"}, "b then a and b"},
		{"Online: {0}", []any{3}, "Online: 3"},
		{"{0} {1} {x} {} {+0} {-1}", []any{"a"}, "a {1} {x} {} {+0} {-1}"},
		{"100% {0", []any{"a"}, "100% {0"},
		{"{{0}}", []any{"a"}, "{a}"},
	} {
		if got := fill(c.in, c.args); got != c.want {
			t.Errorf("fill(%q, %v) = %q, want %q", c.in, c.args, got, c.want)
		}
	}
}

func TestBase(t *testing.T) {
	for in, want := range map[string]string{
		"nl_NL.UTF-8": "nl", "nl-BE": "nl", "en": "en", "C": "en", "POSIX": "en",
		"C.UTF-8": "en", "de_DE@euro": "de", " NL ": "nl",
	} {
		if got := base(in); got != want {
			t.Errorf("base(%q) = %q, want %q", in, got, want)
		}
	}
}

// apps are the directories, relative to this package, whose sources call Trans.
var apps = []string{"../../client-windows", "../../client-linux", "../../desk"}

var placeholder = regexp.MustCompile(`\{[0-9]+\}`)

// TestCatalogs checks every embedded catalog: it parses, each key is an
// i18n.Trans literal in the apps' sources, and each translation uses the
// same placeholders as its English key.
func TestCatalogs(t *testing.T) {
	used := usedKeys(t)
	entries, err := locales.ReadDir("locales")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		l := strings.TrimSuffix(e.Name(), ".json")
		c, err := readCatalog(l)
		if err != nil {
			t.Error(err)
			continue
		}
		for k, v := range c {
			if !used[k] {
				t.Errorf("%s.json: %q is not used by any i18n.Trans call", l, k)
			}
			if !samePlaceholders(k, v) {
				t.Errorf("%s.json: %q has other placeholders than %q", l, v, k)
			}
		}
	}
}

// TestDefaultInstruction checks that the apps translate the same text the
// server documents as the default instruction.
func TestDefaultInstruction(t *testing.T) {
	if !usedKeys(t)[control.DefaultInstruction] {
		t.Errorf("no i18n.Trans call uses control.DefaultInstruction's text")
	}
}

func samePlaceholders(a, b string) bool {
	set := func(s string) []string {
		p := placeholder.FindAllString(s, -1)
		slices.Sort(p)
		return slices.Compact(p)
	}
	return slices.Equal(set(a), set(b))
}

// usedKeys returns the first argument of every i18n.Trans call in the apps.
// A first argument that is not a string constant is an error, since it could
// not be checked.
func usedKeys(t *testing.T) map[string]bool {
	used := map[string]bool{}
	fset := token.NewFileSet()
	for _, dir := range apps {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
				return err
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Trans" {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "i18n" {
					return true
				}
				if s, ok := constString(call.Args[0]); ok {
					used[s] = true
				} else {
					t.Errorf("%s: i18n.Trans needs a string literal", fset.Position(call.Pos()))
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return used
}

// constString evaluates a string literal or a + of string literals.
func constString(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return constString(e.X)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		a, ok1 := constString(e.X)
		b, ok2 := constString(e.Y)
		return a + b, ok1 && ok2
	}
	return "", false
}
