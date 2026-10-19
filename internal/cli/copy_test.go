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
func copyNames() []string {
	return []string{"API key", "AI Gateway key", "SSH key", "S4", "HTTP", "TLS", "AI Gateway", "GPU"}
}

// formatCalls name the calls whose string argument at the given index is a
// format that reaches output, and whether that text is an error message or
// a hint (held to the error rules: lowercase, no final period).
func formatCalls() map[string]struct {
	arg  int
	kind string // "error", "hint" or ""
} {
	return map[string]struct {
		arg  int
		kind string // "error", "hint" or ""
	}{
		"Sprintf": {0, ""}, "Printf": {0, ""}, "Errorf": {0, ""}, "Fprintf": {1, ""}, "Appendf": {1, ""},
		"New": {2, "error"}, "HTTP": {3, "error"}, "WithHint": {0, "hint"},
		"tell": {0, ""}, "say": {0, ""}, "hintLine": {0, "hint"},
	}
}

// TestCopyRules scans the strings in the CLI's Go sources that reach output:
// output is plain ASCII (no em dash, ellipsis, middle dot or arrow), says
// "for example" rather than "e.g.", never single-quotes a saladcloud command,
// quotes names with plain double quotes rather than %q, and an error or hint
// starts lowercase (or with a name) and ends without a period.
func TestCopyRules(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	err := walkProductionGoFiles("..", fset, func(f *ast.File) {
		formats, patterns := copyLiteralRoles(f)
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || patterns[lit] {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			checkCopyLiteral(t, fset.Position(lit.Pos()).String(), lit, s, formats[lit])
			return true
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

// walkProductionGoFiles parses every Go file under root that is not a test, into fset, and calls fn with each.
func walkProductionGoFiles(root string, fset *token.FileSet, fn func(f *ast.File)) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		fn(f)
		return nil
	})
}

// copyCallName is the name of the function or method call calls: "Errorf" for fmt.Errorf, "say" for say.
func copyCallName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name
	case *ast.Ident:
		return fn.Name
	}
	return ""
}

// copyLiteralRoles finds the string literals in f that formatCalls take as a format, with each one's role ("error",
// "hint" or "format"), and the literals compiled as regular expressions, which are patterns rather than output.
func copyLiteralRoles(f *ast.File) (map[*ast.BasicLit]string, map[*ast.BasicLit]bool) {
	formats := make(map[*ast.BasicLit]string) // literal -> its role
	patterns := make(map[*ast.BasicLit]bool)  // regular expressions
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := copyCallName(call)
		if name == "MustCompile" || name == "Compile" { // a pattern, not output
			markCopyPatterns(call, patterns)
		}
		if lit, role := copyFormatLiteral(call, name); lit != nil {
			formats[lit] = role
		}
		return true
	})
	return formats, patterns
}

// markCopyPatterns marks every literal argument of call, a regular expression compile, as a pattern.
func markCopyPatterns(call *ast.CallExpr, patterns map[*ast.BasicLit]bool) {
	for _, a := range call.Args {
		if lit, isLit := a.(*ast.BasicLit); isLit {
			patterns[lit] = true
		}
	}
}

// copyFormatLiteral is the string literal call, a call to name, takes as a format when formatCalls lists name, and
// its role ("error", "hint" or "format"); nil when there is none.
func copyFormatLiteral(call *ast.CallExpr, name string) (*ast.BasicLit, string) {
	fc, ok := formatCalls()[name]
	if !ok || len(call.Args) <= fc.arg {
		return nil, ""
	}
	lit, ok := call.Args[fc.arg].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return nil, ""
	}
	role := fc.kind
	if role == "" {
		role = "format"
	}
	return lit, role
}

// checkCopyLiteral holds the string s of literal lit, at position at, to the copy rules: every string is ASCII, says
// "for example" and leaves commands bare; a format (role not "") never uses %q; an error or hint format starts
// lowercase (or with a name) and ends without a period.
func checkCopyLiteral(t *testing.T, at string, lit *ast.BasicLit, s, role string) {
	t.Helper()
	checkCopyASCII(t, at, lit, s)
	if strings.Contains(s, "e.g.") {
		t.Errorf("%s: %q says e.g.; write for example", at, s)
	}
	if strings.Contains(s, "'saladcloud") {
		t.Errorf("%s: %q single-quotes a command; commands are bare", at, s)
	}
	if role == "" {
		return
	}
	if strings.Contains(s, "%q") {
		t.Errorf("%s: %q uses %%q; quote with plain double quotes", at, s)
	}
	if role != "error" && role != "hint" || s == "" || strings.HasPrefix(s, "%") {
		return
	}
	checkCopyMessageCase(t, at, s, role)
}

// checkCopyASCII reports the first rune of s outside ASCII, unless lit spells a byte or rune escape: that is a
// deliberate value (a BOM to strip), never prose.
func checkCopyASCII(t *testing.T, at string, lit *ast.BasicLit, s string) {
	t.Helper()
	if strings.Contains(lit.Value, `\x`) || strings.Contains(lit.Value, `\u`) {
		return
	}
	for _, r := range s {
		if r > unicode.MaxASCII {
			t.Errorf("%s: %q is not ASCII (%q)", at, s, r)
			return
		}
	}
}

// checkCopyMessageCase holds an error or hint format s to the error rules: no final period, and no leading capital
// unless s starts with one of copyNames.
func checkCopyMessageCase(t *testing.T, at, s, role string) {
	t.Helper()
	if strings.HasSuffix(s, ".") {
		t.Errorf("%s: the %s %q ends with a period", at, role, s)
	}
	if unicode.IsUpper(rune(s[0])) {
		named := false
		for _, n := range copyNames() {
			named = named || strings.HasPrefix(s, n)
		}
		if !named {
			t.Errorf("%s: the %s %q starts with a capital", at, role, s)
		}
	}
}
