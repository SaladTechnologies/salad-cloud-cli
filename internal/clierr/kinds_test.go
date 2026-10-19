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
			vs := assertAs[*ast.ValueSpec](t, s)
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
	}
	return out
}

func TestKindsListsEveryConstant(t *testing.T) {
	consts := declared(t, "Kind")
	if len(Kinds) != len(consts) {
		t.Errorf("Kinds has %d entries, clierr.go declares %d Kind constants", len(Kinds), len(consts))
	}
	seen := make(map[Kind]bool)
	for _, k := range Kinds {
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
	kindConsts := declared(t, "Kind")
	var pairs []pair
	fset := token.NewFileSet()
	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Hidden directories hold other checkouts (.claude/worktrees)
			// and tooling, not this module's code; go ./... skips them too.
			name := d.Name()
			if path != moduleRoot &&
				(strings.HasPrefix(name, ".") || name == "dist" || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		inClierr := f.Name.Name == "clierr"
		// ref reports whether e names clierr's identifier name ("clierr.X",
		// or bare "X" inside the package).
		ref := func(e ast.Expr) (string, bool) {
			switch x := e.(type) {
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok && id.Name == "clierr" {
					return x.Sel.Name, true
				}
			case *ast.Ident:
				if inClierr {
					return x.Name, true
				}
			}
			return "", false
		}
		checkKind := func(e ast.Expr) {
			pos := fset.Position(e.Pos()).String()
			if lit, ok := e.(*ast.BasicLit); ok {
				t.Errorf("%s: kind %s is a literal; use a clierr.Kind constant", pos, lit.Value)
				return
			}
			if name, ok := ref(e); ok && strings.HasPrefix(name, "Kind") && name != "Kind" {
				if _, ok := kindConsts[name]; !ok {
					t.Errorf("%s: %s is not a declared kind", pos, name)
				}
			}
		}
		isKindType := func(e ast.Expr) bool { name, ok := ref(e); return ok && name == "Kind" }
		isKindField := func(e ast.Expr) bool { sel, ok := e.(*ast.SelectorExpr); return ok && sel.Sel.Name == "Kind" }
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				// A function returning a Kind returns constants.
				if x.Body == nil || x.Type.Results == nil {
					break
				}
				for i, res := range x.Type.Results.List {
					if !isKindType(res.Type) {
						continue
					}
					ast.Inspect(x.Body, func(n ast.Node) bool {
						if r, ok := n.(*ast.ReturnStmt); ok && i < len(r.Results) {
							checkKind(r.Results[i])
						}
						return true
					})
				}
			case *ast.BinaryExpr:
				// err.Kind == "..." compares against a hand-spelled kind.
				if x.Op == token.EQL || x.Op == token.NEQ {
					if isKindField(x.X) {
						checkKind(x.Y)
					}
					if isKindField(x.Y) {
						checkKind(x.X)
					}
				}
			case *ast.AssignStmt:
				for i, l := range x.Lhs {
					if isKindField(l) && i < len(x.Rhs) {
						checkKind(x.Rhs[i])
					}
				}
			case *ast.CallExpr:
				name, ok := ref(x.Fun)
				if !ok {
					break
				}
				switch name {
				case "Kind":
					if !inClierr {
						t.Errorf("%s: conversion to clierr.Kind; use a constant", fset.Position(x.Pos()))
					}
				case "New", "HTTP":
					if len(x.Args) < 2 {
						break
					}
					checkKind(x.Args[1])
					code, cok := ref(x.Args[0])
					kind, kok := ref(x.Args[1])
					if cok && kok && strings.HasPrefix(code, "Code") && strings.HasPrefix(kind, "Kind") {
						pairs = append(pairs, pair{code, kind, fset.Position(x.Pos()).String()})
					}
				}
			case *ast.CompositeLit:
				if name, ok := ref(x.Type); !ok || name != "Error" {
					break
				}
				for _, el := range x.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Kind" {
							checkKind(kv.Value)
						}
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) < 50 {
		t.Fatalf("found only %d clierr.New/HTTP sites; is the walk reaching internal/?", len(pairs))
	}
	checkGuide(t, kindConsts, pairs)
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
	table := make(map[string]string) // kind value -> exit code cell
	inKinds := false                 // only the Error kinds section: other tables name other things
	for line := range strings.SplitSeq(string(data), "\n") {
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
func assertAs[T any](t *testing.T, v any) T {
	t.Helper()
	x, ok := v.(T)
	if !ok {
		t.Fatalf("got %T, want %T", v, x)
	}
	return x
}
