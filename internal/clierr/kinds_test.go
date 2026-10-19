package clierr

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

const moduleRoot = "../.."

// declared returns the constants clierr.go declares with the given prefix,
// name to value: the Code* ints and the Kind* strings.
func declared(t *testing.T, prefix string) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "clierr.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string)
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, s := range g.Specs {
			addConstants(t, assertAs[*ast.ValueSpec](t, s), prefix, out)
		}
	}
	return out
}

// addConstants adds to out the constants vs declares with the given prefix, name to value (a string unquoted).
func addConstants(t *testing.T, vs *ast.ValueSpec, prefix string, out map[string]string) {
	t.Helper()
	for i, n := range vs.Names {
		if !strings.HasPrefix(n.Name, prefix) || i >= len(vs.Values) {
			continue
		}
		lit, ok := vs.Values[i].(*ast.BasicLit)
		if !ok {
			t.Fatalf("%s is not a literal", n.Name)
		}
		v := lit.Value
		if lit.Kind == token.STRING {
			v = must(strconv.Unquote(v))(t)
		}
		out[n.Name] = v
	}
}

func TestKindsListsEveryConstant(t *testing.T) {
	t.Parallel()
	consts := declared(t, "Kind")
	if len(Kinds()) != len(consts) {
		t.Errorf("Kinds has %d entries, clierr.go declares %d Kind constants", len(Kinds()), len(consts))
	}
	seen := make(map[Kind]bool)
	for _, k := range Kinds() {
		if seen[k] {
			t.Errorf("Kinds lists %q twice", k)
		}
		seen[k] = true
	}
	for name, v := range consts {
		if !seen[Kind(v)] {
			t.Errorf("Kinds is missing %s (%q)", name, v)
		}
	}
}

// pair is one call site's exit code constant and kind constant.
type pair struct{ code, kind, pos string }

// TestKindCallSites walks every non-test Go file in the module: a kind is
// always one of the constants, never a literal or a conversion, so the set
// stays closed. Kind is a named type, so a non-constant string cannot become
// one without a conversion; literals are the only way past the compiler,
// and this test closes them at clierr.New and clierr.HTTP, Error literals,
// Kind-returning functions, and comparisons and assignments to a Kind
// field. The constant code/kind pairs are then checked against the guide.
func TestKindCallSites(t *testing.T) {
	t.Parallel()
	kindConsts := declared(t, "Kind")
	paths, err := moduleSources(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	c := &siteChecker{t: t, fset: token.NewFileSet(), kindConsts: kindConsts, inClierr: false, pairs: nil}
	for _, path := range paths {
		f, err := parser.ParseFile(c.fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		c.inClierr = f.Name.Name == "clierr"
		ast.Inspect(f, c.visit)
	}
	if len(c.pairs) < 50 {
		t.Fatalf("found only %d clierr.New/HTTP sites; is the walk reaching internal/?", len(c.pairs))
	}
	checkGuide(t, kindConsts, c.pairs)
}

// moduleSources lists the non-test Go files under root, in walk order.
func moduleSources(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}

// skippedDir reports whether the walk skips a directory: hidden directories hold other checkouts
// (.claude/worktrees) and tooling, not this module's code, and go ./... skips them too, as it does the others.
func skippedDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "dist" || name == "vendor" || name == "node_modules"
}

// siteChecker checks the kinds in one parsed file at a time, and collects the constant code/kind pairs passed to
// clierr.New and clierr.HTTP across them.
type siteChecker struct {
	t          *testing.T
	fset       *token.FileSet
	kindConsts map[string]string
	inClierr   bool // the file is clierr's own, where its identifiers are bare
	pairs      []pair
}

// ref reports whether e names clierr's identifier name ("clierr.X", or bare "X" inside the package).
func (c *siteChecker) ref(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && id.Name == "clierr" {
			return x.Sel.Name, true
		}
	case *ast.Ident:
		if c.inClierr {
			return x.Name, true
		}
	}
	return "", false
}

// checkKind fails the test when e, an expression used as a kind, is a literal or names an undeclared Kind constant.
func (c *siteChecker) checkKind(e ast.Expr) {
	pos := c.fset.Position(e.Pos()).String()
	if lit, ok := e.(*ast.BasicLit); ok {
		c.t.Errorf("%s: kind %s is a literal; use a clierr.Kind constant", pos, lit.Value)
		return
	}
	if name, ok := c.ref(e); ok && strings.HasPrefix(name, "Kind") && name != "Kind" {
		if _, ok := c.kindConsts[name]; !ok {
			c.t.Errorf("%s: %s is not a declared kind", pos, name)
		}
	}
}

// isKindType reports whether e names the clierr.Kind type.
func (c *siteChecker) isKindType(e ast.Expr) bool { name, ok := c.ref(e); return ok && name == "Kind" }

// isKindField reports whether e selects a field named Kind.
func isKindField(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Kind"
}

// visit is the ast.Inspect visitor: it checks each node a hand-spelled kind can appear in.
func (c *siteChecker) visit(n ast.Node) bool {
	switch x := n.(type) {
	case *ast.FuncDecl:
		c.checkKindResults(x)
	case *ast.BinaryExpr:
		c.checkComparison(x)
	case *ast.AssignStmt:
		for i, l := range x.Lhs {
			if isKindField(l) && i < len(x.Rhs) {
				c.checkKind(x.Rhs[i])
			}
		}
	case *ast.CallExpr:
		c.checkCall(x)
	case *ast.CompositeLit:
		c.checkErrorLiteral(x)
	}
	return true
}

// checkKindResults checks that a function returning a Kind returns constants.
func (c *siteChecker) checkKindResults(x *ast.FuncDecl) {
	if x.Body == nil || x.Type.Results == nil {
		return
	}
	for i, res := range x.Type.Results.List {
		if !c.isKindType(res.Type) {
			continue
		}
		ast.Inspect(x.Body, func(n ast.Node) bool {
			if r, ok := n.(*ast.ReturnStmt); ok && i < len(r.Results) {
				c.checkKind(r.Results[i])
			}
			return true
		})
	}
}

// checkComparison checks err.Kind == "...", a comparison against a hand-spelled kind.
func (c *siteChecker) checkComparison(x *ast.BinaryExpr) {
	if x.Op != token.EQL && x.Op != token.NEQ {
		return
	}
	if isKindField(x.X) {
		c.checkKind(x.Y)
	}
	if isKindField(x.Y) {
		c.checkKind(x.X)
	}
}

// checkCall checks a conversion to clierr.Kind outside clierr, and the kind passed to clierr.New and clierr.HTTP,
// collecting the pair when both code and kind are constants.
func (c *siteChecker) checkCall(x *ast.CallExpr) {
	name, ok := c.ref(x.Fun)
	if !ok {
		return
	}
	switch name {
	case "Kind":
		if !c.inClierr {
			c.t.Errorf("%s: conversion to clierr.Kind; use a constant", c.fset.Position(x.Pos()))
		}
	case "New", "HTTP":
		if len(x.Args) < 2 {
			return
		}
		c.checkKind(x.Args[1])
		code, cok := c.ref(x.Args[0])
		kind, kok := c.ref(x.Args[1])
		if cok && kok && strings.HasPrefix(code, "Code") && strings.HasPrefix(kind, "Kind") {
			c.pairs = append(c.pairs, pair{code, kind, c.fset.Position(x.Pos()).String()})
		}
	}
}

// checkErrorLiteral checks the Kind field of a clierr.Error literal.
func (c *siteChecker) checkErrorLiteral(x *ast.CompositeLit) {
	if name, ok := c.ref(x.Type); !ok || name != "Error" {
		return
	}
	for _, el := range x.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Kind" {
			c.checkKind(kv.Value)
		}
	}
}

var kindRow = regexp.MustCompile("^\\| `([a-z_]+)` \\| ([^|]+) \\|")

// checkGuide asserts docs/guide.md's kind table has exactly one row per
// kind, and that each call site's exit code is the one the table gives.
func checkGuide(t *testing.T, kindConsts map[string]string, pairs []pair) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRoot, "docs", "guide.md"))
	if err != nil {
		t.Fatal(err)
	}
	table := guideKinds(t, string(data))
	values := make(map[string]bool)
	for _, v := range kindConsts {
		values[v] = true
		if _, ok := table[v]; !ok {
			t.Errorf("docs/guide.md has no row for kind %q", v)
		}
	}
	for k := range table {
		if !values[k] {
			t.Errorf("docs/guide.md lists kind %q, which clierr does not declare", k)
		}
	}
	codes := declared(t, "Code")
	for _, p := range pairs {
		want := table[kindConsts[p.kind]]
		if _, err := strconv.Atoi(want); err != nil {
			continue // "the child's", not a fixed code
		}
		if got := codes[p.code]; got != want {
			t.Errorf("%s: %s with %s (exit %s); the guide gives kind %q exit %s",
				p.pos, p.code, p.kind, got, kindConsts[p.kind], want)
		}
	}
}

// guideKinds reads the guide's Error kinds table, kind value to exit code cell, and fails the test on a kind listed
// twice. Only that section counts: other tables name other things.
func guideKinds(t *testing.T, guide string) map[string]string {
	t.Helper()
	table := make(map[string]string)
	inKinds := false
	for line := range strings.SplitSeq(guide, "\n") {
		if strings.HasPrefix(line, "#") {
			inKinds = strings.TrimSpace(line) == "### Error kinds"
		}
		m := kindRow.FindStringSubmatch(line)
		if m == nil || !inKinds {
			continue
		}
		if _, dup := table[m[1]]; dup {
			t.Errorf("guide lists kind %q twice", m[1])
		}
		table[m[1]] = strings.TrimSpace(m[2])
	}
	return table
}

// must returns v and fails the test when err is set, on the test goroutine:
// must(os.ReadFile(p))(t).
func must[T any](v T, err error) func(*testing.T) T {
	return func(t *testing.T) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

// assertAs returns v as a T and fails the test when it holds something else.
func assertAs[T any](t *testing.T, v any) T { //nolint:ireturn // returns the caller's type parameter
	t.Helper()
	x, ok := v.(T)
	if !ok {
		t.Fatalf("got %T, want %T", v, x)
	}
	return x
}
