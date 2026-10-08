package cli

import (
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

func TestDescribeNotFound(t *testing.T) {
	tests := []struct {
		name string
		in   *clierr.Error
		want string
	}{
		{
			name: "bare Not Found is replaced",
			in:   &clierr.Error{Code: clierr.CodeNotFound, Kind: clierr.KindNotFound, Message: "Not Found", HTTPStatus: ptr.To(404)},
			want: `container group "demo" not found in acme/prod`,
		},
		{
			name: "empty message is replaced",
			in:   &clierr.Error{Code: clierr.CodeNotFound, Kind: clierr.KindNotFound, Message: "", HTTPStatus: ptr.To(404)},
			want: `container group "demo" not found in acme/prod`,
		},
		{
			name: "bare HTTP 404 fallback is replaced",
			in:   &clierr.Error{Code: clierr.CodeNotFound, Kind: clierr.KindNotFound, Message: "HTTP 404", HTTPStatus: ptr.To(404)},
			want: `container group "demo" not found in acme/prod`,
		},
		{
			name: "HTML fallback is replaced",
			in:   &clierr.Error{Code: clierr.CodeNotFound, Kind: clierr.KindNotFound, Message: "HTTP 404 (non-JSON error response)", HTTPStatus: ptr.To(404)},
			want: `container group "demo" not found in acme/prod`,
		},
		{
			name: "Not Found with only its problem type is replaced",
			in:   &clierr.Error{Code: clierr.CodeNotFound, Kind: clierr.KindNotFound, Message: "Not Found (type: project_not_found_exception)", HTTPStatus: ptr.To(404)},
			want: `container group "demo" not found in acme/prod`,
		},
		{
			name: "richer API message gets the context appended",
			in:   &clierr.Error{Code: clierr.CodeNotFound, Kind: clierr.KindNotFound, Message: "The container group was deleted on 2026-01-01", HTTPStatus: ptr.To(404)},
			want: `The container group was deleted on 2026-01-01 (container group "demo" not found in acme/prod)`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeNotFound(tt.in, "", `container group "%s" not found in %s/%s`, "demo", "acme", "prod")
			if got == nil {
				t.Fatal("describeNotFound returned nil")
			}
			if got.Message != tt.want {
				t.Errorf("message = %q, want %q", got.Message, tt.want)
			}
			if got.Code != clierr.CodeNotFound || got.Kind != clierr.KindNotFound || ptr.Deref(got.HTTPStatus) != 404 {
				t.Errorf("code/kind/status changed: %+v", got)
			}
		})
	}
}

// TestGroupNotFoundEverywhere: every command that names a group reports a
// missing one the same way, cp and ssh included (they list the group's
// instances first, and used to print the API's bare "Not Found").
func TestGroupNotFoundEverywhere(t *testing.T) {
	want := `container group "nosuch" not found in my-org/default`
	for _, args := range [][]string{
		{"cg", "get", "nosuch"},
		{"cg", "cp", "nosuch:/etc/hostname", "./x"},
		{"cg", "ssh", "nosuch"},
		{"cg", "sftp", "nosuch"},
		{"cg", "logs", "nosuch", "--instance", "8a2f"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stubAPI(t, map[string]string{quotasRoute: `{}`}) // the organization exists
			_, err := runRoot(t, args...)
			cerr, ok := err.(*clierr.Error)
			if !ok || cerr.Code != clierr.CodeNotFound || cerr.Message != want ||
				cerr.HintText() != "to list the container groups, run saladcloud container-groups list" {
				t.Errorf("%v (hint %q), want (4) %s", err, cerr.HintText(), want)
			}
		})
	}
}

func TestDescribeNotFoundNon404Untouched(t *testing.T) {
	in := &clierr.Error{Code: clierr.CodeAuth, Kind: clierr.KindAuth, Message: "Unauthorized", HTTPStatus: ptr.To(401)}
	got := describeNotFound(in, "", `container group "%s" not found in %s/%s`, "demo", "acme", "prod")
	if got != in {
		t.Error("non-404 error was not returned unchanged")
	}
	if got.Message != "Unauthorized" {
		t.Errorf("message = %q, want %q", got.Message, "Unauthorized")
	}
}

func TestDescribeNotFoundNil(t *testing.T) {
	if got := describeNotFound(nil, "", "context %s", "x"); got != nil {
		t.Errorf("describeNotFound(nil) = %v, want nil", got)
	}
}

func TestDescribeNotFoundDoesNotMutateInput(t *testing.T) {
	in := &clierr.Error{Code: clierr.CodeNotFound, Kind: clierr.KindNotFound, Message: "Not Found", HTTPStatus: ptr.To(404)}
	_ = describeNotFound(in, "", `container group "%s" not found in %s/%s`, "demo", "acme", "prod")
	if in.Message != "Not Found" {
		t.Errorf("input error was mutated to %q", in.Message)
	}
}

// TestDescribeNotFoundSetsTheHint: the one fix replaces any other; with
// none, the error keeps what it had.
func TestDescribeNotFoundSetsTheHint(t *testing.T) {
	nf := clierr.New(clierr.CodeNotFound, clierr.KindNotFound, "Not Found").WithHint("old")
	if got := describeNotFound(nf, "new", "x"); got.HintText() != "new" {
		t.Errorf("hint %q, want new", got.HintText())
	}
	if got := describeNotFound(nf, "", "x"); got.HintText() != "old" {
		t.Errorf("hint %q, want old", got.HintText())
	}
}
