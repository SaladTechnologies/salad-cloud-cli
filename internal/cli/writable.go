// get --writable: the response, reduced to what update -f will accept.
//
// The common edit workflow is "show me the config, change one thing, apply
// it". Piping `get -o json` straight back into `update -f` fails on a dozen
// response-only fields (id, version, current_state …) and one relocation
// (responses return priority at the top level; requests take
// container.priority), and the strict rejection message can only explain
// that after the fact. --writable removes the explanation: it emits the
// document the patch schema accepts, so `get --writable | update -f -`
// round-trips cleanly and `jq` edits compose. It is derived from the SDK
// types, not a hand-kept list, so it tracks the schema.
package cli

import (
	"reflect"
	"sort"
	"strings"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"

	"github.com/saladtechnologies/salad-cloud-cli/internal/input"
)

// writableDocument renders g as a patch document: the known
// request/response relocations applied, then every path the patch schema
// does not know removed. It returns the document tree and the sorted dotted
// paths that were dropped, for the stderr summary.
func writableDocument(g *shared.ContainerGroup) (any, []string, error) {
	tree, err := toTree(g)
	if err != nil {
		return nil, nil, err
	}
	obj, ok := tree.(map[string]any)
	if !ok {
		return tree, nil, nil
	}
	// Relocate: a field that responses spell one way and requests another
	// is moved to its request path before filtering, so it survives as the
	// patch schema expects rather than being dropped as unknown.
	for reqPath, respPath := range requestToResponsePath {
		v, present := lookupPath(obj, respPath)
		if !present {
			continue
		}
		if _, already := lookupPath(obj, reqPath); !already {
			setPath(obj, reqPath, v)
		}
		deletePath(obj, respPath)
	}
	var dropped []string
	filtered := input.FilterTree(obj, reflect.TypeFor[containergroups.ContainerGroupPatch](), "", &dropped)
	sort.Strings(dropped)
	return filtered, dropped, nil
}

// setPath assigns v at a dotted path in a generic tree, creating
// intermediate objects as needed.
func setPath(tree map[string]any, path string, v any) {
	segs := strings.Split(path, ".")
	cur := tree
	for _, seg := range segs[:len(segs)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[seg] = next
		}
		cur = next
	}
	cur[segs[len(segs)-1]] = v
}

// deletePath removes a dotted path from a generic tree; a missing prefix is
// a no-op.
func deletePath(tree map[string]any, path string) {
	segs := strings.Split(path, ".")
	cur := tree
	for _, seg := range segs[:len(segs)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	delete(cur, segs[len(segs)-1])
}
