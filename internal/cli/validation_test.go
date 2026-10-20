package cli

import (
	"go/ast"
	"go/token"
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

// stringArg is the string literal at args[i], if it is one: a single literal or literals joined with +, which a long
// message is split into to keep its lines short.
func stringArg(args []ast.Expr, i int) (string, bool) {
	if len(args) <= i {
		return "", false
	}
	return stringLiteral(args[i])
}

// stringLiteral is the value of e when e is a string literal or a concatenation of them.
func stringLiteral(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		left, ok := stringLiteral(e.X)
		if !ok {
			return "", false
		}
		right, ok := stringLiteral(e.Y)
		return left + right, ok
	case *ast.ParenExpr:
		return stringLiteral(e.X)
	default:
		return "", false
	}
}

// validationSites are the validation sites in the non-test Go files under root.
func validationSites(t *testing.T, root string) []validationSite {
	t.Helper()
	var sites []validationSite
	fset := token.NewFileSet()
	err := walkProductionGoFiles(root, fset, func(f *ast.File) {
		hints := validationHints(f)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if code, format, ok := validationCall(call); ok {
				sites = append(sites, validationSite{fset.Position(call.Pos()).String(), code, format, hints[call]})
			}
			return true
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return sites
}

// validationHints maps each call in f that has a literal hint chained onto it (.WithHint("...")) to that hint.
func validationHints(f *ast.File) map[*ast.CallExpr]string {
	hints := make(map[*ast.CallExpr]string) // a New call -> the hint chained onto it
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WithHint" {
			return true
		}
		if inner, ok := sel.X.(*ast.CallExpr); ok {
			if h, ok := stringArg(call.Args, 0); ok {
				hints[inner] = h
			}
		}
		return true
	})
	return hints
}

// validationCall reports whether call is a New(CodeUsage|CodeInvalidInput|CodeAuth, kind, "format", ...) with a
// literal format, and if so its exit code and format.
func validationCall(call *ast.CallExpr) (int, string, bool) {
	codes := map[string]int{"CodeUsage": 2, "CodeInvalidInput": 6, "CodeAuth": 3}
	if len(call.Args) < 3 {
		return 0, "", false
	}
	if sel, isSel := call.Fun.(*ast.SelectorExpr); !isSel || sel.Sel.Name != "New" {
		return 0, "", false
	}
	codeSel, ok := call.Args[0].(*ast.SelectorExpr)
	if !ok {
		return 0, "", false
	}
	code, ok := codes[codeSel.Sel.Name]
	if !ok {
		return 0, "", false
	}
	format, ok := stringArg(call.Args, 2)
	if !ok {
		return 0, "", false
	}
	return code, format, true
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
	for _, s := range validationSites(t, "..") {
		if genericFormat.MatchString(s.format) {
			continue
		}
		checkValidationSite(t, s, matchValidationRows(rows, s))
	}
	for _, s := range validationSites(t, "..") {
		for _, r := range matchValidationRows(rows, s) {
			r.used = true
		}
	}
	for _, r := range rows {
		if !r.used {
			t.Errorf("validation table row %q matches no check in internal/; remove or fix it", r.fragment)
		}
	}
}

// matchValidationRows are the rows whose fragment is in the site's text in the field the row names.
func matchValidationRows(rows []*validationRow, s validationSite) []*validationRow {
	var out []*validationRow
	for _, r := range rows {
		if t := s.text(r.field); t != "" && strings.Contains(t, r.fragment) {
			out = append(out, r)
		}
	}
	return out
}

// checkValidationSite checks the site against the rows it matched, marking them used: it needs at least one (unless
// it reports the API's answer about a key), and every one must give the site's exit code.
func checkValidationSite(t *testing.T, s validationSite, matched []*validationRow) {
	t.Helper()
	if len(matched) == 0 {
		if s.code == 3 && afterAnAnswer(s.format) {
			return // the API's answer about a key, not a check made before any request
		}
		t.Errorf("%s: %q (exit %d) has no row in AGENTS.md's validation table", s.pos, s.format, s.code)
		return
	}
	for _, r := range matched {
		r.used = true
		if r.code != s.code {
			t.Errorf("%s: %q exits %d; the table row %q says %d", s.pos, s.format, s.code, r.fragment, r.code)
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
