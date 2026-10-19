package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// validationRow is one row of AGENTS.md's validation table: the exit code,
// the field of the error the fragment is in (message or hint), and the
// fragment.
type validationRow struct {
	code     int
	field    string
	fragment string
	used     bool
}

var tableRow = regexp.MustCompile("^\\| .* \\| (\\d+) \\| .* \\| (message|hint) \\| `([^`]+)` \\|$")

func readValidationTable(t *testing.T) []*validationRow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []*validationRow
	in := false
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "## Validation table"):
			in = true
		case in && strings.HasPrefix(line, "## "):
			in = false
		case in:
			if m := tableRow.FindStringSubmatch(line); m != nil {
				code := must(strconv.Atoi(m[1]))(t)
				rows = append(rows, &validationRow{code: code, field: m[2], fragment: m[3]})
			}
		}
	}
	if len(rows) < 40 {
		t.Fatalf("read %d validation rows from AGENTS.md; is the table there?", len(rows))
	}
	return rows
}

// genericFormat matches a rewrap such as "%v" or "%s: %v": verbs and
// punctuation only, so the check is someone else's. Any word in the format
// makes it a check of its own, which needs a row.
var genericFormat = regexp.MustCompile(`^(%[a-zA-Z]|[\s:;,.])*$`)

// validationSite is one clierr.New(CodeUsage|CodeInvalidInput|CodeAuth, ...),
// with the hint chained onto it (.WithHint("...")) when there is one.
type validationSite struct {
	pos    string
	code   int
	format string
	hint   string
}

// text is the site's text in the field a row names.
func (s validationSite) text(field string) string {
	if field == "hint" {
		return s.hint
	}
	return s.format
}

// stringArg is the string literal at args[i], if it is one.
func stringArg(args []ast.Expr, i int) (string, bool) {
	if len(args) <= i {
		return "", false
	}
	lit, ok := args[i].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

func validationSites(t *testing.T, root string) []validationSite {
	t.Helper()
	codes := map[string]int{"CodeUsage": 2, "CodeInvalidInput": 6, "CodeAuth": 3}
	var sites []validationSite
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		hints := make(map[*ast.CallExpr]string) // a New call -> the hint chained onto it
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "WithHint" {
				if inner, ok := sel.X.(*ast.CallExpr); ok {
					if h, ok := stringArg(call.Args, 0); ok {
						hints[inner] = h
					}
				}
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 3 {
				return true
			}
			if sel, isSel := call.Fun.(*ast.SelectorExpr); !isSel || sel.Sel.Name != "New" {
				return true
			}
			codeSel, ok := call.Args[0].(*ast.SelectorExpr)
			if !ok {
				return true
			}
			code, ok := codes[codeSel.Sel.Name]
			if !ok {
				return true
			}
			format, ok := stringArg(call.Args, 2)
			if !ok {
				return true
			}
			sites = append(sites, validationSite{fset.Position(call.Pos()).String(), code, format, hints[call]})
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sites
}

// TestValidationTable ties every client-side check in internal/ to a row
// of AGENTS.md's validation table: each usage, invalid-input or auth error
// matches a row by a fragment of the field the row names (its message or its
// hint), and every matching row gives its code.
// Rows must also still match a check somewhere in internal/, so the table
// cannot keep a check that is gone.
func TestValidationTable(t *testing.T) {
	t.Parallel()
	rows := readValidationTable(t)
	match := func(s validationSite) []*validationRow {
		var out []*validationRow
		for _, r := range rows {
			if t := s.text(r.field); t != "" && strings.Contains(t, r.fragment) {
				out = append(out, r)
			}
		}
		return out
	}
	for _, s := range validationSites(t, "..") {
		if genericFormat.MatchString(s.format) {
			continue
		}
		matched := match(s)
		if len(matched) == 0 {
			if s.code == 3 && afterAnAnswer(s.format) {
				continue // the API's answer about a key, not a check made before any request
			}
			t.Errorf("%s: %q (exit %d) has no row in AGENTS.md's validation table", s.pos, s.format, s.code)
			continue
		}
		for _, r := range matched {
			r.used = true
			if r.code != s.code {
				t.Errorf("%s: %q exits %d; the table row %q says %d", s.pos, s.format, s.code, r.fragment, r.code)
			}
		}
	}
	for _, s := range validationSites(t, "..") {
		for _, r := range match(s) {
			r.used = true
		}
	}
	for _, r := range rows {
		if !r.used {
			t.Errorf("validation table row %q matches no check in internal/; remove or fix it", r.fragment)
		}
	}
}

// TestValidationSitesReadHints: a hint chained onto a check is read as the
// site's hint, so a row can name the hint as the field its fragment is in.
func TestValidationSitesReadHints(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := `package x

func f() {
	_ = clierr.New(clierr.CodeUsage, clierr.KindUsage, "no organization set").WithHint("pass --org")
	_ = clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "invalid --cpu %d", 0)
}
`
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	sites := validationSites(t, dir)
	if len(sites) != 2 {
		t.Fatalf("found %d sites, want 2: %+v", len(sites), sites)
	}
	if s := sites[0]; s.format != "no organization set" || s.hint != "pass --org" || s.text("hint") != "pass --org" ||
		s.text("message") != s.format {
		t.Errorf("site with a hint = %+v", s)
	}
	if s := sites[1]; s.hint != "" || s.text("hint") != "" {
		t.Errorf("site without a hint = %+v", s)
	}
}

// afterAnAnswer reports whether an exit-3 message reports what the API
// answered about a key (a 401 or 403), which no check before a request
// could know; every other exit-3 check needs a row.
func afterAnAnswer(format string) bool {
	for _, f := range []string{"was rejected", "is invalid", "has no AI Gateway access"} {
		if strings.Contains(format, f) {
			return true
		}
	}
	return false
}
