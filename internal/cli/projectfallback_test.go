package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// The project falls back to "default" only when nothing sets one, and then
// says so: a warning on stderr when a command uses it, and "no valid
// project set" (exit 2) when the organization has no project by that name.

const (
	quotasRoute       = "/quotas"
	defaultListRoute  = "/projects/default/containers"
	emptyListBody     = `{"items":[]}`
	assumedWarningTag = `warning: using project "default" because none was set`
)

// unsetProject clears the project stubAPI sets, so the fallback applies.
func unsetProject(t *testing.T) {
	t.Helper()
	t.Setenv("SALAD_PROJECT", "")
}

// TestAssumedProjectWarnsOnce: a project-scoped command on the fallback
// prints one warning, and none when the project was set.
func TestAssumedProjectWarnsOnce(t *testing.T) {
	stubAPI(t, map[string]string{defaultListRoute: emptyListBody})
	unsetProject(t)
	stderr := captureStderr(t, func() {
		if _, err := runRoot(t, "container-groups", "list", "-o", "json"); err != nil {
			t.Fatal(err)
		}
	})
	if n := strings.Count(stderr, assumedWarningTag); n != 1 {
		t.Errorf("warning printed %d times, want once:\n%s", n, stderr)
	}

	t.Setenv("SALAD_PROJECT", "default")
	stderr = captureStderr(t, func() {
		if _, err := runRoot(t, "container-groups", "list", "-o", "json"); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(stderr, "warning: using project") {
		t.Errorf("a project that was set must not warn:\n%s", stderr)
	}
}

// TestAssumedProjectWarningIsOrgScopedOnly: org-scoped commands never use a
// project, so they never warn about one.
func TestAssumedProjectWarningIsOrgScopedOnly(t *testing.T) {
	stubAPI(t, map[string]string{quotasRoute: `{}`})
	unsetProject(t)
	stderr := captureStderr(t, func() {
		if _, err := runRoot(t, "quotas", "get", "-o", "json"); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(stderr, "warning: using project") {
		t.Errorf("an org-scoped command warned about a project:\n%s", stderr)
	}
}

// TestAssumedProjectMissing: on the fallback, a 404 because the
// organization has no "default" project is exit 2, "no valid project set",
// with where to find names; a missing group in an existing "default" stays
// not found, and so does a 404 whose organization is the problem.
func TestAssumedProjectMissing(t *testing.T) {
	cases := []struct {
		name     string
		routes   map[string]string
		args     []string
		wantCode int
		want     string
	}{
		{
			"list, no default project",
			map[string]string{quotasRoute: `{}`},
			[]string{"container-groups", "list"},
			clierr.CodeUsage, "no valid project set",
		},
		{
			"get, no default project",
			map[string]string{quotasRoute: `{}`},
			[]string{"container-groups", "get", "my-app"},
			clierr.CodeUsage, "no valid project set",
		},
		{
			"queues list, no default project",
			map[string]string{quotasRoute: `{}`},
			[]string{"queues", "list"},
			clierr.CodeUsage, "no valid project set",
		},
		{
			"get, default exists but the group does not",
			map[string]string{quotasRoute: `{}`, defaultListRoute: emptyListBody},
			[]string{"container-groups", "get", "my-app"},
			clierr.CodeNotFound, `container group "my-app" not found`,
		},
		{
			"get, the organization is not visible",
			make(map[string]string),
			[]string{"container-groups", "get", "my-app"},
			clierr.CodeNotFound, `organization "my-org" (from SALAD_ORGANIZATION) not found`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubAPI(t, tc.routes)
			unsetProject(t)
			_, err := runRoot(t, tc.args...)
			var ce *clierr.Error
			ok := errors.As(err, &ce)
			if !ok || ce.Code != tc.wantCode || !strings.Contains(ce.Message, tc.want) {
				t.Fatalf("err = %v, want code %d containing %q", err, tc.wantCode, tc.want)
			}
			if tc.wantCode == clierr.CodeUsage {
				if ce.Message != `no valid project set: my-org has no "default" project` ||
					ce.HintText() != "create one at https://portal.salad.com/organizations/my-org/projects/new, then "+
						"pass --project "+
						"or run saladcloud use my-org/<project>" {
					t.Errorf("message %q, hint %q", ce.Message, ce.HintText())
				}
			}
		})
	}
}

// TestSavedProjectStaysWithItsOrg: a saved project is not carried to
// another organization named by a flag; the fallback applies instead, and
// its warning names the project it skipped.
func TestSavedProjectStaysWithItsOrg(t *testing.T) {
	stubAPI(
		t,
		map[string]string{
			"/projects/default/containers": emptyListBody,
			"/projects/prod/containers":    `{"items":[{"name":"x"}]}`,
		},
	)
	unsetProject(t)
	t.Setenv("SALAD_ORGANIZATION", "")
	if err := config.Save(procEnv, config.File{Organization: "org-a", Project: "prod"}); err != nil {
		t.Fatal(err)
	}
	stderr := captureStderr(t, func() {
		if _, err := runRoot(t, "container-groups", "list", "--org", "org-b", "-o", "json"); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stderr, `none was set for organization "org-b" (the saved project "prod" is for "org-a")`) {
		t.Errorf("warning does not name the skipped project:\n%s", stderr)
	}
}

// TestGroupLogsCheckTheAssumedProject: a group's logs are filtered by its
// project, which the log query never checks, so on the fallback the command
// warns, and exits 2 when the organization has no "default" instead of
// returning nothing.
func TestGroupLogsCheckTheAssumedProject(t *testing.T) {
	for _, args := range [][]string{
		{"container-groups", "logs", "g", "--since", "5m", "-o", "json"},
		{"logs", "-c", "g", "--since", "5m", "-o", "json"},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			stubAPI(
				t,
				map[string]string{quotasRoute: `{}`, defaultListRoute: emptyListBody, "/log-entries": `{"items":[]}`},
			)
			unsetProject(t)
			stderr := captureStderr(t, func() {
				if _, err := runRoot(t, args...); err != nil {
					t.Fatal(err)
				}
			})
			if strings.Count(stderr, assumedWarningTag) != 1 {
				t.Errorf("want one assumed-project warning:\n%s", stderr)
			}

			stubAPI(t, map[string]string{quotasRoute: `{}`, "/log-entries": `{"items":[]}`})
			unsetProject(t)
			_, err := runRoot(t, args...)
			var ce *clierr.Error
			if !errors.As(err, &ce) || ce.Code != clierr.CodeUsage ||
				!strings.Contains(ce.Message, "no valid project set") {
				t.Errorf("err = %v, want exit 2, no valid project set", err)
			}
		})
	}
}
