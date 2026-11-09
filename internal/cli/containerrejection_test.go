package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// TestFindContainerRejection: a container change that did not land is found
// by either signal, and nothing else is. The fixtures follow what the API was
// seen to do: a rejected version leaves the group on its old version with
// "Version N: <reason>", an applied one moves version to N and clears the
// description, and the report stays on the group through later writes.
func TestFindContainerRejection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		patch       string
		before      string
		after       string
		wantFields  []string // nil: not rejected, or rejected with no field to show
		rejected    bool
		description string
	}{
		{
			name:   "rejected image",
			patch:  `{"container":{"image":"busybox:no-such-tag"}}`,
			before: `{"name":"g","version":1,"container":{"image":"busybox:1.36"}}`,
			after: `{"name":"g","version":1,"container":{"image":"busybox:1.36"},` +
				`"current_state":{"description":"Version 2: Manifest Not Found"}}`,
			wantFields:  []string{"container.image"},
			rejected:    true,
			description: "Version 2: Manifest Not Found",
		},
		{
			// The incident: a private image sent without its credentials, with
			// GPU classes and replicas in the same patch. Replicas applied; the
			// container's fields stayed where they were.
			name: "private image sent without credentials",
			patch: `{"container":{"image":"docker.io/me/app:rc2","resources":{"gpu_classes":["c"]}},` +
				`"replicas":4}`,
			before: `{"name":"g","version":1,"replicas":2,` +
				`"container":{"image":"docker.io/me/app:rc1","resources":{"gpu_classes":["a","b"]}}}`,
			after: `{"name":"g","version":1,"replicas":4,` +
				`"container":{"image":"docker.io/me/app:rc1","resources":{"gpu_classes":["a","b"]}},` +
				`"current_state":{"description":"Version 2: Image Not Found, Check Image Name And Tag"}}`,
			wantFields:  []string{"container.image", "container.resources.gpu_classes"},
			rejected:    true,
			description: "Version 2: Image Not Found, Check Image Name And Tag",
		},
		{
			name:   "applied image",
			patch:  `{"container":{"image":"busybox:1.37"}}`,
			before: `{"name":"g","version":1,"container":{"image":"busybox:1.36"}}`,
			after: `{"name":"g","version":4,"container":{"image":"busybox:1.37"},` +
				`"current_state":{"description":""}}`,
			rejected: false,
		},
		{
			// A resend stages a new version number, so its report is new.
			name:  "resend of a rejected image",
			patch: `{"container":{"image":"busybox:no-such-tag"}}`,
			before: `{"name":"g","version":1,"container":{"image":"busybox:1.36"},` +
				`"current_state":{"description":"Version 2: Manifest Not Found"}}`,
			after: `{"name":"g","version":1,"container":{"image":"busybox:1.36"},` +
				`"current_state":{"description":"Version 3: Manifest Not Found"}}`,
			wantFields:  []string{"container.image"},
			rejected:    true,
			description: "Version 3: Manifest Not Found",
		},
		{
			// The report outlives the write that caused it.
			name:  "old report under a write that stages nothing",
			patch: `{"display_name":"renamed"}`,
			before: `{"name":"g","version":1,"display_name":"g","container":{"image":"busybox:1.36"},` +
				`"current_state":{"description":"Version 3: Manifest Not Found"}}`,
			after: `{"name":"g","version":1,"display_name":"renamed","container":{"image":"busybox:1.36"},` +
				`"current_state":{"description":"Version 3: Manifest Not Found"}}`,
			rejected: false,
		},
		{
			// No response shows registry credentials, so only the report
			// tells that their version was rejected.
			name:   "rejection the diff cannot see",
			patch:  `{"container":{"registry_authentication":{"basic":{"username":"u","password":"p"}}}}`,
			before: `{"name":"g","version":1,"container":{"image":"docker.io/me/app:rc1"}}`,
			after: `{"name":"g","version":1,"container":{"image":"docker.io/me/app:rc1"},` +
				`"current_state":{"description":"Version 2: Image Not Found, Check Image Name And Tag"}}`,
			wantFields:  nil,
			rejected:    true,
			description: "Version 2: Image Not Found, Check Image Name And Tag",
		},
		{
			name:   "report of a version the group already runs",
			patch:  `{"display_name":"renamed"}`,
			before: `{"name":"g","version":2,"display_name":"g"}`,
			after: `{"name":"g","version":2,"display_name":"renamed",` +
				`"current_state":{"description":"Version 2: Deployed"}}`,
			rejected: false,
		},
		{
			name:   "pending report",
			patch:  `{"container":{"image":"busybox:1.37"}}`,
			before: `{"name":"g","version":1,"container":{"image":"busybox:1.37"}}`,
			after: `{"name":"g","version":1,"container":{"image":"busybox:1.37"},` +
				`"current_state":{"description":"Version 2 is pending."}}`,
			rejected: false,
		},
		{
			// An environment variable left as it was is enough, with no
			// report at all.
			name:       "environment variable left as it was",
			patch:      `{"container":{"environment_variables":{"A":"2"}}}`,
			before:     `{"name":"g","version":1,"container":{"image":"x","environment_variables":{"A":"1"}}}`,
			after:      `{"name":"g","version":1,"container":{"image":"x","environment_variables":{"A":"1"}}}`,
			wantFields: []string{"container.environment_variables.A"},
			rejected:   true,
		},
		{
			// Dropped on every write: a warning, not a rejection.
			name:     "image_caching left as it was",
			patch:    `{"container":{"image_caching":false}}`,
			before:   `{"name":"g","version":1,"container":{"image":"x","image_caching":true}}`,
			after:    `{"name":"g","version":1,"container":{"image":"x","image_caching":true}}`,
			rejected: false,
		},
		{
			name:     "priority on a CPU-only group",
			patch:    `{"container":{"priority":"low"}}`,
			before:   `{"name":"g","version":1,"priority":"batch"}`,
			after:    `{"name":"g","version":1,"priority":"batch"}`,
			rejected: false,
		},
		{
			// Moved, just not to the value asked for: normalization at worst,
			// and still a warning.
			name:     "container field that moved elsewhere",
			patch:    `{"container":{"resources":{"memory":1000}}}`,
			before:   `{"name":"g","version":1,"container":{"resources":{"memory":512}}}`,
			after:    `{"name":"g","version":2,"container":{"resources":{"memory":1024}}}`,
			rejected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			before, after := grp(t, tt.before), grp(t, tt.after)
			ignored := findIgnoredFields([]byte(tt.patch), before, after)
			r, rejected := findContainerRejection(before, after, ignored)
			if rejected != tt.rejected {
				t.Fatalf("rejected = %v, want %v (ignored %+v)", rejected, tt.rejected, ignored)
			}
			paths := make([]string, 0, len(r.Fields))
			for _, f := range r.Fields {
				paths = append(paths, f.Path)
			}
			if strings.Join(paths, ",") != strings.Join(tt.wantFields, ",") {
				t.Errorf("fields = %v, want %v", paths, tt.wantFields)
			}
			if r.Description != tt.description {
				t.Errorf("description = %q, want %q", r.Description, tt.description)
			}
		})
	}
}

// TestFindContainerRejectionGuards: no verdict without both states, or while
// the change is still pending (the after-state is the previous config then).
func TestFindContainerRejectionGuards(t *testing.T) {
	t.Parallel()
	before := grp(t, `{"name":"g","version":1,"container":{"image":"x"}}`)
	after := grp(t, `{"name":"g","version":1,"container":{"image":"x"},`+
		`"current_state":{"description":"Version 2: Manifest Not Found"}}`)
	field := []ignoredField{{Path: "container.image", Requested: `"y"`, Actual: `"x"`, Moved: false}}
	if _, rejected := findContainerRejection(nil, after, field); rejected {
		t.Error("rejected with no before-state")
	}
	if _, rejected := findContainerRejection(before, nil, field); rejected {
		t.Error("rejected with no after-state")
	}
	after.PendingChange = new(true)
	if _, rejected := findContainerRejection(before, after, field); rejected {
		t.Error("rejected while the change is pending")
	}
}

// credentialHint is the hint for a new image sent without registry
// credentials. Its advice is create --from's, in the same words.
const credentialHint = "check the image name and tag; registry credentials are never returned by the API, " +
	"so for a private image, pass them again with -f <file> holding container.registry_authentication"

// TestContainerRejectionError: exit 6 with the API's reason, and the
// credential advice whenever the patch changes the image without
// credentials, on Docker Hub as anywhere else.
func TestContainerRejectionError(t *testing.T) {
	t.Parallel()
	const reason = "Version 2: Image Not Found, Check Image Name And Tag"
	const getHint = "to see what the group runs now, run saladcloud container-groups get my-app"
	tests := []struct {
		name        string
		patch       string
		description string
		wantMessage string
		wantHint    string
	}{
		{
			name:        "new image, no credentials",
			patch:       `{"container":{"image":"docker.io/me/app:rc2"}}`,
			description: reason,
			wantMessage: "the API did not apply the container change to my-app: " + reason,
			wantHint:    credentialHint,
		},
		{
			name:        "Docker Hub image without a registry host",
			patch:       `{"container":{"image":"me/app:rc2"},"replicas":4}`,
			description: reason,
			wantMessage: "the API did not apply the container change to my-app: " + reason,
			wantHint:    credentialHint,
		},
		{
			name: "new image with credentials",
			patch: `{"container":{"image":"docker.io/me/app:rc2",` +
				`"registry_authentication":{"basic":{"username":"u","password":"p"}}}}`,
			description: reason,
			wantMessage: "the API did not apply the container change to my-app: " + reason,
			wantHint:    getHint,
		},
		{
			name:        "no image change, no report",
			patch:       `{"container":{"environment_variables":{"A":"2"}}}`,
			description: "",
			wantMessage: "the API did not apply the container change to my-app",
			wantHint:    getHint,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := containerRejection{Fields: nil, Description: tt.description}
			cerr := containerRejectionError("my-app", []byte(tt.patch), r)
			if cerr.Code != clierr.CodeInvalidInput || cerr.Kind != clierr.KindInvalidInput || cerr.HTTPStatus != nil {
				t.Errorf("code %d, kind %s, http_status %v; want 6, invalid_input, none",
					cerr.Code, cerr.Kind, cerr.HTTPStatus)
			}
			if cerr.Message != tt.wantMessage {
				t.Errorf("message = %q, want %q", cerr.Message, tt.wantMessage)
			}
			if cerr.HintText() != tt.wantHint {
				t.Errorf("hint = %q, want %q", cerr.HintText(), tt.wantHint)
			}
		})
	}
}

// The group rejectionAPI serves, before the PATCH and settled after it.
const (
	rejectionBefore = `{"name":"my-app","version":1,"pending_change":false,` +
		`"container":{"image":"docker.io/library/busybox:1.36"},` +
		`"current_state":{"status":"running","description":""}}`
	rejectionPending = `{"name":"my-app","version":1,"pending_change":true,` +
		`"container":{"image":"docker.io/library/busybox:1.36"},` +
		`"current_state":{"status":"running","description":"Version 2 is pending."}}`
	rejectionRejected = `{"name":"my-app","version":1,"pending_change":false,` +
		`"container":{"image":"docker.io/library/busybox:1.36"},` +
		`"current_state":{"status":"running","description":"Version 2: Manifest Not Found"}}`
	rejectionApplied = `{"name":"my-app","version":2,"pending_change":false,` +
		`"container":{"image":"docker.io/library/busybox:1.37"},` +
		`"current_state":{"status":"running","description":""}}`
)

// rejectionAPI serves one group to update: rejectionBefore until the PATCH,
// which answers pending, and settled on every read after it.
func rejectionAPI(t *testing.T, settled string) *httptest.Server {
	t.Helper()
	var patched atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/containers/my-app") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch {
		case r.Method == http.MethodPatch:
			patched.Store(true)
			respond(t, w, rejectionPending)
		case patched.Load():
			respond(t, w, settled)
		default:
			respond(t, w, rejectionBefore)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// rejectionRun is a run against srv with a key, an organization and a project
// set.
func rejectionRun(t *testing.T, srv *httptest.Server) *testRun {
	t.Helper()
	app := newTestRun(t)
	app.setenv("SALAD_CLI_API_BASE_URL", srv.URL)
	app.setenv("SALAD_API_KEY", "salad_cloud_user_test0000")
	app.setenv("SALAD_ORGANIZATION", "my-org")
	app.setenv("SALAD_PROJECT", "default")
	return app
}

// TestUpdateRejectedImageFails: an image the platform rejects once the
// change settles exits 6 with the API's reason and the credential advice,
// and stdout stays empty, so a script cannot mistake it for success.
func TestUpdateRejectedImageFails(t *testing.T) {
	t.Parallel()
	app := rejectionRun(t, rejectionAPI(t, rejectionRejected))
	stdout, err := app.runRoot(t, "container-groups", "update", "my-app",
		"--image", "docker.io/library/busybox:no-such-tag", "-o", "json")
	var cerr *clierr.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %v, want a *clierr.Error", err)
	}
	if cerr.Code != clierr.CodeInvalidInput || cerr.Kind != clierr.KindInvalidInput {
		t.Errorf("code %d, kind %s; want 6, invalid_input", cerr.Code, cerr.Kind)
	}
	want := "the API did not apply the container change to my-app: Version 2: Manifest Not Found"
	if cerr.Message != want {
		t.Errorf("message = %q, want %q", cerr.Message, want)
	}
	if cerr.HintText() != credentialHint {
		t.Errorf("hint = %q, want the credential advice", cerr.HintText())
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing on a failed update", stdout)
	}
}

// TestUpdateAppliedImageSucceeds: the same update, applied, emits the settled
// group and exits 0.
func TestUpdateAppliedImageSucceeds(t *testing.T) {
	t.Parallel()
	app := rejectionRun(t, rejectionAPI(t, rejectionApplied))
	stdout, err := app.runRoot(t, "container-groups", "update", "my-app",
		"--image", "docker.io/library/busybox:1.37", "-o", "json")
	if err != nil {
		t.Fatalf("update = %v, want success", err)
	}
	if !strings.Contains(stdout, `"image": "docker.io/library/busybox:1.37"`) {
		t.Errorf("stdout = %s, want the settled group", stdout)
	}
}
