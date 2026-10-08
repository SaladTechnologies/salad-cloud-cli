package cli

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/input"
)

func TestParseFromRef(t *testing.T) {
	for _, tc := range []struct {
		ref, project, group string
	}{
		{"my-app", "", "my-app"},
		{"staging/my-app", "staging", "my-app"},
	} {
		project, group, cerr := parseFromRef(tc.ref)
		if cerr != nil || project != tc.project || group != tc.group {
			t.Errorf("parseFromRef(%q) = %q, %q, %v; want %q, %q", tc.ref, project, group, cerr, tc.project, tc.group)
		}
	}
	for _, ref := range []string{"", "/my-app", "staging/", "my-org/staging/my-app"} {
		if _, _, cerr := parseFromRef(ref); cerr == nil || cerr.Code != clierr.CodeUsage {
			t.Errorf("parseFromRef(%q) = %v, want a usage error", ref, cerr)
		}
	}
	// The cross-organization form says why it is refused.
	_, _, cerr := parseFromRef("my-org/staging/my-app")
	if !strings.Contains(errText(cerr), "organization") {
		t.Errorf("cross-org message %q does not mention the organization", errText(cerr))
	}
}

func TestCheckCopyTarget(t *testing.T) {
	for _, tc := range []struct {
		same    bool
		name    string
		wantErr bool
	}{
		{true, "", true},
		{true, "my-app", true},
		{true, "my-copy", false},
		{false, "", false}, // another project: the source's name is free here
		{false, "my-app", false},
	} {
		if cerr := checkCopyTarget(tc.same, tc.name, "my-app"); (cerr != nil) != tc.wantErr {
			t.Errorf("checkCopyTarget(%v, %q) = %v, wantErr %v", tc.same, tc.name, cerr, tc.wantErr)
		}
	}
}

// richResponse plus a queue connection, which create takes and update does not.
func copySource(t *testing.T) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(richResponse), &m); err != nil {
		t.Fatal(err)
	}
	m["queue_connection"] = map[string]any{"path": "/job", "port": 8080, "queue_name": "jobs"}
	m["restart_policy"] = "on_failure"
	b, _ := json.Marshal(m)
	return string(b)
}

func TestCopyDocument(t *testing.T) {
	raw, err := copyDocument(grp(t, copySource(t)), "my-copy", false)
	if err != nil {
		t.Fatalf("copyDocument: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	// The create schema accepts the whole document.
	if u := input.UnknownFieldList(raw, reflect.TypeOf(containergroups.ContainerGroupCreationRequest{})); len(u) > 0 {
		t.Errorf("copy has fields create rejects: %v", u)
	}
	for _, gone := range []string{
		"id", "organization_name", "project_name", "version", "current_state", "readme", "pending_change",
		"autostart_policy", "priority", "container.hash", "container.size", "networking.dns",
	} {
		if _, has := lookupPath(doc, gone); has {
			t.Errorf("copy still has %q", gone)
		}
	}
	for path, want := range map[string]any{
		"container.priority":                "high",
		"restart_policy":                    "on_failure",
		"queue_connection.queue_name":       "jobs",
		"networking.auth":                   true,
		"networking.protocol":               "http",
		"container.environment_variables.A": "1",
		"container.image":                   "docker.io/library/busybox:1.36",
		"display_name":                      "my-copy",
		"scheduled-scaling-enabled":         true,
		"container.resources.memory":        float64(1024),
	} {
		if got, _ := lookupPath(doc, path); !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, want %#v", path, got, want)
		}
	}
	env, _ := lookupPath(doc, "container.environment_variables")
	if env.(map[string]any)["weird.KEY-2"] != "x" {
		t.Errorf("env var with a dotted key not copied: %v", env)
	}
}

func TestCopyDocumentDisplayName(t *testing.T) {
	g := grp(t, copySource(t))
	for _, tc := range []struct {
		name       string
		displaySet bool
		want       string
	}{
		{"my-copy", false, "my-copy"},
		{"my-copy", true, "My App"}, // --display-name applies later, over the copy
		{"", false, "My App"},       // same name in another project
		{"my-app", false, "My App"},
	} {
		raw, err := copyDocument(g, tc.name, tc.displaySet)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		if doc["display_name"] != tc.want {
			t.Errorf("copyDocument(%q, %v) display_name = %v, want %q", tc.name, tc.displaySet, doc["display_name"], tc.want)
		}
	}
}

func TestCopyDocumentBuildsARequest(t *testing.T) {
	raw, err := copyDocument(grp(t, copySource(t)), "my-copy", false)
	if err != nil {
		t.Fatal(err)
	}
	var base containergroups.ContainerGroupCreationRequest
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatalf("copy does not unmarshal into a creation request: %v", err)
	}
	req, cerr := buildCreateRequest(base, createFlags{Name: "my-copy", HasBase: true})
	if cerr != nil {
		t.Fatalf("buildCreateRequest: %v", cerr)
	}
	if cerr := validateCreateRequest(&req); cerr != nil {
		t.Fatalf("validateCreateRequest: %v", cerr)
	}
	if *req.Name != "my-copy" || *req.Replicas != 2 || req.QueueConnection == nil {
		t.Errorf("request lost the copy: name=%v replicas=%v queue=%v", *req.Name, *req.Replicas, req.QueueConnection)
	}
	// autostart_policy was not copied, so create's default applies.
	if req.AutostartPolicy == nil || !*req.AutostartPolicy {
		t.Errorf("autostart_policy = %v, want create's default true", req.AutostartPolicy)
	}
}

func TestNoteRegistryCredentials(t *testing.T) {
	const private = "registry.example.com/team/app:1"
	var buf bytes.Buffer
	noteRegistryCredentials(&buf, nil, private)
	if !strings.Contains(buf.String(), "registry_authentication") {
		t.Errorf("no note without a file: %q", buf.String())
	}
	buf.Reset()
	noteRegistryCredentials(&buf, json.RawMessage(`{"container":{"environment_variables":{"A":"2"}}}`), private)
	if buf.Len() == 0 {
		t.Error("no note for a file without credentials")
	}
	buf.Reset()
	noteRegistryCredentials(&buf, json.RawMessage(`{"container":{"registry_authentication":{"basic":{"username":"u","password":"p"}}}}`), private)
	if buf.Len() != 0 {
		t.Errorf("note printed although the file supplies credentials: %q", buf.String())
	}
	// A public registry's image needs no credentials; an image the CLI
	// cannot place gets the note.
	for image, note := range map[string]bool{
		"docker.io/library/busybox:1.36": false, "busybox:1.36": false, "me/app:latest": false,
		"ghcr.io/me/app:latest": false, "public.ecr.aws/x/y:1": false, "quay.io/x/y": false,
		private: true, "localhost:5000/app": true, "myregistry:5000/app": true, "": true,
	} {
		buf.Reset()
		noteRegistryCredentials(&buf, nil, image)
		if (buf.Len() > 0) != note {
			t.Errorf("image %q: note %q, want a note %v", image, buf.String(), note)
		}
	}
}

func TestPlural(t *testing.T) {
	if plural(1, "replica") != "1 replica" || plural(0, "replica") != "0 replicas" || plural(3, "replica") != "3 replicas" {
		t.Errorf("plural: %q %q %q", plural(1, "replica"), plural(0, "replica"), plural(3, "replica"))
	}
}
