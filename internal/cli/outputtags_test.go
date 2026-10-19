package cli

import (
	"go/ast"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// omitemptyAllowed are the CLI's structs that are never emitted, so leaving
// unset keys out is right for them: what they encode is not output.
func omitemptyAllowed() map[string]string {
	return map[string]string{
		"config.File":             "the config file on disk: an unset value is left out of the file",
		"api.AvailabilityRequest": "a request body: an unset requirement is not sent",
	}
}

// TestNoOmitemptyInCLIStructs: the CLI's own structs encode what the code
// set, and output fills a key the encoder dropped with null, which means
// "unknown". An omitempty tag would turn a real false, 0 or "" into that
// null, so no struct the CLI owns may carry one, in internal/cli or in the
// packages whose models it emits directly (internal/api's OrganizationList,
// say), except the few that are never output. (The SDK's structs do, and
// output's fill handles them.)
func TestNoOmitemptyInCLIStructs(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	checked := 0
	used := make(map[string]bool)
	err := walkProductionGoFiles("..", fset, func(f *ast.File) {
		ast.Inspect(f, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := spec.Type.(*ast.StructType)
			if !ok {
				return true
			}
			checked += checkOmitemptyStruct(t, fset, f.Name.Name+"."+spec.Name.Name, st, used)
			return true
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 80 {
		t.Fatalf("checked only %d tagged fields; is the walk reaching internal/?", checked)
	}
	for name := range omitemptyAllowed() {
		if !used[name] {
			t.Errorf("%s is allowed omitempty but no longer has any; remove it from the list", name)
		}
	}
}

// checkOmitemptyStruct reports each field of st, the struct named name (package.Type), whose json tag has
// omitempty, unless omitemptyAllowed lists name, which it then marks in used. It returns how many tagged fields it
// checked.
func checkOmitemptyStruct(
	t *testing.T,
	fset *token.FileSet,
	name string,
	st *ast.StructType,
	used map[string]bool,
) int {
	t.Helper()
	checked := 0
	for _, field := range st.Fields.List {
		if field.Tag == nil {
			continue
		}
		tag, err := strconv.Unquote(field.Tag.Value)
		if err != nil {
			continue
		}
		checked++
		if !strings.Contains(reflect.StructTag(tag).Get("json"), "omitempty") {
			continue
		}
		if _, ok := omitemptyAllowed()[name]; ok {
			used[name] = true
			continue
		}
		t.Errorf(
			"%s: %s's json tag %q has omitempty; a CLI struct encodes every key it sets",
			fset.Position(field.Pos()),
			name,
			tag,
		)
	}
	return checked
}
