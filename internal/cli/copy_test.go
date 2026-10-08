package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// copyNames are the names a message may start with in capitals.
var copyNames = []string{"API key", "AI Gateway key", "SSH key", "S4", "HTTP", "TLS", "AI Gateway", "GPU"}

// formatCalls name the calls whose string argument at the given index is a
// format that reaches output, and whether that text is an error message or
// a hint (held to the error rules: lowercase, no final period).
var formatCalls = map[string]struct {
	arg  int
	kind string // "error", "hint" or ""
}{
	"Sprintf": {0, ""}, "Printf": {0, ""}, "Errorf": {0, ""}, "Fprintf": {1, ""}, "Appendf": {1, ""},
	"New": {2, "error"}, "HTTP": {3, "error"}, "WithHint": {0, "hint"},
	"tell": {0, ""}, "say": {0, ""}, "hintLine": {0, "hint"},
}

// TestCopyRules scans the strings in the CLI's Go sources that reach output:
// output is plain ASCII (no em dash, ellipsis, middle dot or arrow), says
// "for example" rather than "e.g.", never single-quotes a saladcloud command,
// quotes names with plain double quotes rather than %q, and an error or hint
// starts lowercase (or with a name) and ends without a period.
func TestCopyRules(t *testing.T) {
	fset := token.NewFileSet()
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		formats := map[*ast.BasicLit]string{} // literal -> its role
		patterns := map[*ast.BasicLit]bool{}  // regular expressions
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				name = fn.Sel.Name
			case *ast.Ident:
				name = fn.Name
			}
			if name == "MustCompile" || name == "Compile" { // a pattern, not output
				for _, a := range call.Args {
					if lit, ok := a.(*ast.BasicLit); ok {
						patterns[lit] = true
					}
				}
			}
			fc, ok := formatCalls[name]
			if !ok || len(call.Args) <= fc.arg {
				return true
			}
			if lit, ok := call.Args[fc.arg].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				role := fc.kind
				if role == "" {
					role = "format"
				}
				formats[lit] = role
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || patterns[lit] {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			at := fset.Position(lit.Pos()).String()
			// A byte or rune escape is a deliberate value (a BOM to strip),
			// never prose.
			escaped := strings.Contains(lit.Value, `\x`) || strings.Contains(lit.Value, `\u`)
			for _, r := range s {
				if escaped {
					break
				}
				if r > unicode.MaxASCII {
					t.Errorf("%s: %q is not ASCII (%q)", at, s, r)
					break
				}
			}
			if strings.Contains(s, "e.g.") {
				t.Errorf("%s: %q says e.g.; write for example", at, s)
			}
			if strings.Contains(s, "'saladcloud") {
				t.Errorf("%s: %q single-quotes a command; commands are bare", at, s)
			}
			role, isFormat := formats[lit]
			if !isFormat {
				return true
			}
			if strings.Contains(s, "%q") {
				t.Errorf("%s: %q uses %%q; quote with plain double quotes", at, s)
			}
			if role != "error" && role != "hint" || s == "" || strings.HasPrefix(s, "%") {
				return true
			}
			if strings.HasSuffix(s, ".") {
				t.Errorf("%s: the %s %q ends with a period", at, role, s)
			}
			if unicode.IsUpper(rune(s[0])) {
				named := false
				for _, n := range copyNames {
					named = named || strings.HasPrefix(s, n)
				}
				if !named {
					t.Errorf("%s: the %s %q starts with a capital", at, role, s)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
