package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/inferenceendpoints"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/logs"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/organizationdata"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/organizations"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/queues"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/quotas"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/webhooksecretkey"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
)

// The JSON contract rides on the SDK's types, so an SDK bump that adds,
// drops or renames a key changes what scripts read without any CLI code
// changing. This test lists every key path each emitted type can carry and
// compares the list with testdata/json-keys.golden: a difference fails CI
// until the golden file is regenerated and the CHANGELOG says what
// changed. Regenerate it with `go test ./internal/cli -run TestJSONKeySnapshot -update-keys`.
var updateKeys = flag.Bool(
	"update-keys",
	false,
	"rewrite testdata/json-keys.golden",
) //nolint:gochecknoglobals // a test flag registers when the package loads

// keySnapshotRoots are the documents commands emit, by command.
func keySnapshotRoots() []struct {
	name string
	v    any
} {
	return []struct {
		name string
		v    any
	}{
		{"agent init", agentInitResult{}},
		{"ai-gateway login", gatewayLoginResult{}},
		{"ai-gateway logout", gatewayLogoutResult{}},
		{"ai-gateway models list", gatewayModelsResult{}},
		{"ai-gateway test", gatewayTestResult{}},
		{"availability (cpu)", cpuAvailabilityResult{}},
		{"availability (gpu)", gpuAvailabilityResult{}},
		{"changelog", changelogResult{}},
		{"config get", configGetResult{}},
		{"config list", configListResult{}},
		{"config path", configPathResult{}},
		{"config set", configSetResult{}},
		{"config unset", configUnsetResult{}},
		{"container-groups cp", cpResult{}},
		{"container-groups get/create/update", shared.ContainerGroup{}},
		{"container-groups instances get/update", containergroups.ContainerGroupInstance{}},
		{"container-groups start/stop/delete", groupActionResult{}},
		{"container-groups instances restart/recreate/reallocate", instanceActionResult{}},
		{"container-groups scale", scaleResult{}},
		{"doctor", doctorResult{}},
		{"doctor (item)", checkResult{}},
		{"gpus get", organizationdata.GpuClass{}},
		{"inference get", inferenceendpoints.InferenceEndpoint{}},
		{"inference jobs get/create", inferenceendpoints.InferenceEndpointJob{}},
		{"inference jobs cancel", jobActionResult{}},
		{"login", loginResult{}},
		{"logout", logoutResult{}},
		{"logs (entry)", logs.LogEntry{}},
		{"orgs list", api.OrganizationList{}},
		{"queues get/create/update", queues.Queue{}},
		{"queues delete", queueActionResult{}},
		{"queues jobs get/create", queues.QueueJob{}},
		{"queues jobs cancel", queueJobActionResult{}},
		{"quotas get", quotas.Quotas{}},
		{"ssh-key get", sshKeyResult{}},
		{"ssh-key setup", sshKeySetupResult{}},
		{"use", useResult{}},
		{"version", versionInfo{}},
		{"webhook-secret get/rotate", webhooksecretkey.WebhookSecretKey{}},
		{"whoami", whoamiResult{}},
		{"(cpu availability row)", organizations.CpuAvailability{}},
	}
}

func jsonMarshaler() reflect.Type {
	return reflect.TypeFor[json.Marshaler]()
}

// keyPaths appends every key path typ can carry under prefix: "[]" marks an
// array element, "{}" a map value; a type already open on the current path
// is marked "(recursive)" instead of descended again.
func keyPaths(typ reflect.Type, prefix string, open map[reflect.Type]bool, out *[]string) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Implements(jsonMarshaler()) || reflect.PointerTo(typ).Implements(jsonMarshaler()) {
		return // encodes itself: a leaf
	}
	switch typ.Kind() {
	case reflect.Struct:
		if open[typ] {
			*out = append(*out, prefix+" (recursive)")
			return
		}
		open[typ] = true
		defer delete(open, typ)
		var fields func(reflect.Type)
		fields = func(st reflect.Type) {
			for i := range st.NumField() {
				f := st.Field(i)
				tag := f.Tag.Get("json")
				name, _, _ := strings.Cut(tag, ",")
				if tag == "-" {
					continue
				}
				if f.Anonymous && name == "" {
					fields(f.Type)
					continue
				}
				if !f.IsExported() {
					continue
				}
				if name == "" {
					name = f.Name
				}
				p := name
				if prefix != "" {
					p = prefix + "." + name
				}
				*out = append(*out, p)
				keyPaths(f.Type, p, open, out)
			}
		}
		fields(typ)
	case reflect.Slice, reflect.Array:
		if typ.Elem().Kind() != reflect.Uint8 {
			keyPaths(typ.Elem(), prefix+"[]", open, out)
		}
	case reflect.Map:
		keyPaths(typ.Elem(), prefix+"{}", open, out)
	}
}

func TestJSONKeySnapshot(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("# Every JSON key path each command can emit. Generated by TestJSONKeySnapshot;\n")
	b.WriteString("# regenerate with -update-keys, and record any change in CHANGELOG.md.\n")
	for _, root := range keySnapshotRoots() {
		var paths []string
		keyPaths(reflect.TypeOf(root.v), "", make(map[reflect.Type]bool), &paths)
		slices.Sort(paths)
		fmt.Fprintf(&b, "\n[%s]\n", root.name)
		for _, p := range paths {
			b.WriteString(p + "\n")
		}
	}
	got := b.String()
	golden := filepath.Join("testdata", "json-keys.golden")
	if *updateKeys {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v — generate it with: go test ./internal/cli -run TestJSONKeySnapshot -update-keys", err)
	}
	// A Windows checkout may hold the golden file with CRLF endings.
	if w := strings.ReplaceAll(string(want), "\r\n", "\n"); w != got {
		t.Errorf("the JSON key paths changed (an SDK bump or a CLI result type). Review the difference, record it in "+
			"CHANGELOG.md, and regenerate: go test ./internal/cli -run TestJSONKeySnapshot "+
			"-update-keys\n%s", lineDiff(w, got))
	}
}

// lineDiff lists the lines only one side has.
func lineDiff(want, got string) string {
	in := func(s string) map[string]bool {
		m := make(map[string]bool)
		for l := range strings.SplitSeq(s, "\n") {
			m[l] = true
		}
		return m
	}
	w, g := in(want), in(got)
	var out []string
	for l := range w {
		if !g[l] {
			out = append(out, "- "+l)
		}
	}
	for l := range g {
		if !w[l] {
			out = append(out, "+ "+l)
		}
	}
	slices.Sort(out)
	return strings.Join(out, "\n")
}
