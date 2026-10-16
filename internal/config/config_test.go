package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// clearEnv blanks every env var Resolve reads so ambient values on the
// machine running the tests cannot leak in.
func clearEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SALAD_API_KEY", "")
	t.Setenv("SALAD_ORGANIZATION", "")
	t.Setenv("SALAD_PROJECT", "")
	t.Setenv("SALAD_AI_GATEWAY_API_KEY", "")
}

func TestPathHonorsConfigDirOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SALAD_CLI_CONFIG_DIR", dir)
	p, err := Path()
	if err != nil {
		t.Fatalf("Path() error: %v", err)
	}
	want := filepath.Join(dir, "config.json")
	if p != want {
		t.Errorf("Path() = %q, want %q", p, want)
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())
	f, err := Load()
	if err != nil {
		t.Fatalf("Load() on missing file: %v", err)
	}
	if f != (File{}) {
		t.Errorf("Load() = %+v, want zero File", f)
	}
}

func TestLoadCorruptFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SALAD_CLI_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load()
	if err == nil {
		t.Fatal("Load() on corrupt file: want error, got nil")
	}
	// The message names the file and the reason; each command adds the fix
	// (every command loads the config, so a corrupt file blocks even
	// `config set`).
	var re *ReadError
	if !errors.As(err, &re) || !re.Corrupt ||
		!strings.Contains(err.Error(), "cannot read the config file "+filepath.Join(dir, "config.json")) {
		t.Errorf("corrupt-file error %q, want a corrupt ReadError naming the file", err)
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	t.Setenv("SALAD_CLI_CONFIG_DIR", filepath.Join(t.TempDir(), "nested"))
	want := File{APIKey: "salad_cloud_key", Organization: "my-org", Project: "my-project"}
	if err := Save(want); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestResolvePrecedence(t *testing.T) {
	tests := []struct {
		name                string
		fileOrg, envOrg     string
		flagOrg             string
		wantOrg, wantOrgSrc string
	}{
		{
			name:       "flag beats env and config",
			fileOrg:    "file-org",
			envOrg:     "env-org",
			flagOrg:    "flag-org",
			wantOrg:    "flag-org",
			wantOrgSrc: "flag",
		},
		{name: "env beats config", fileOrg: "file-org", envOrg: "env-org", wantOrg: "env-org", wantOrgSrc: "env"},
		{name: "config used when flag and env unset", fileOrg: "file-org", wantOrg: "file-org", wantOrgSrc: "config"},
		{name: "nothing set", wantOrg: "", wantOrgSrc: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())
			if tt.fileOrg != "" {
				if err := Save(File{Organization: tt.fileOrg}); err != nil {
					t.Fatal(err)
				}
			}
			if tt.envOrg != "" {
				t.Setenv("SALAD_ORGANIZATION", tt.envOrg)
			}
			r, err := Resolve(tt.flagOrg, "")
			if err != nil {
				t.Fatalf("Resolve() error: %v", err)
			}
			if r.Organization != tt.wantOrg {
				t.Errorf("Organization = %q, want %q", r.Organization, tt.wantOrg)
			}
			if r.OrganizationSource != tt.wantOrgSrc {
				t.Errorf("OrganizationSource = %q, want %q", r.OrganizationSource, tt.wantOrgSrc)
			}
		})
	}
}

func TestResolveAllFields(t *testing.T) {
	clearEnv(t)
	t.Setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())
	if err := Save(File{APIKey: "file-key", Organization: "file-org", Project: "file-project"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SALAD_API_KEY", "env-key")
	t.Setenv("SALAD_PROJECT", "env-project")

	r, err := Resolve("flag-org", "")
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	// API key has no flag: env beats config.
	if r.APIKey != "env-key" || r.APIKeySource != "env" {
		t.Errorf("APIKey = %q (%q), want env-key (env)", r.APIKey, r.APIKeySource)
	}
	if r.Organization != "flag-org" || r.OrganizationSource != "flag" {
		t.Errorf("Organization = %q (%q), want flag-org (flag)", r.Organization, r.OrganizationSource)
	}
	if r.Project != "env-project" || r.ProjectSource != "env" {
		t.Errorf("Project = %q (%q), want env-project (env)", r.Project, r.ProjectSource)
	}
}

func TestResolveMissingEverything(t *testing.T) {
	clearEnv(t)
	t.Setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())

	r, err := Resolve("", "")
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	// Everything empty — except the project, which falls back to "default"
	// and says so in its source.
	want := Resolved{Project: DefaultProject, ProjectSource: SourceDefault}
	if r != want {
		t.Errorf("Resolve() = %+v, want %+v", r, want)
	}
}

// TestResolveProjectFallback: the "default" fallback is the lowest tier —
// any flag, env, or file value beats it, and the source names it.
func TestResolveProjectFallback(t *testing.T) {
	clearEnv(t)
	t.Setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())

	r, _ := Resolve("", "")
	if r.Project != "default" || r.ProjectSource != SourceDefault {
		t.Errorf("no project anywhere: %q (%q), want default (default)", r.Project, r.ProjectSource)
	}
	t.Setenv("SALAD_PROJECT", "env-project")
	r, _ = Resolve("", "")
	if r.Project != "env-project" || r.ProjectSource != "env" {
		t.Errorf("env should beat the fallback: %q (%q)", r.Project, r.ProjectSource)
	}
	r, _ = Resolve("", "flag-project")
	if r.Project != "flag-project" || r.ProjectSource != "flag" {
		t.Errorf("flag should beat everything: %q (%q)", r.Project, r.ProjectSource)
	}
	t.Setenv("SALAD_PROJECT", "")
	if err := Save(File{Project: "file-project"}); err != nil {
		t.Fatal(err)
	}
	r, _ = Resolve("", "")
	if r.Project != "file-project" || r.ProjectSource != "config" {
		t.Errorf("config should beat the fallback: %q (%q)", r.Project, r.ProjectSource)
	}
	// The organization has no fallback.
	if r.Organization != "" || r.OrganizationSource != "" {
		t.Errorf("organization must not fall back: %q (%q)", r.Organization, r.OrganizationSource)
	}
}

// TestGatewayKey: the AI Gateway key is its own credential — round-trips
// through the file, honors the docs' env var name, env beats config.
func TestGatewayKey(t *testing.T) {
	clearEnv(t)
	t.Setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())
	if err := Save(File{GatewayAPIKey: "file-gw"}); err != nil {
		t.Fatal(err)
	}
	r, err := Resolve("", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.GatewayAPIKey != "file-gw" || r.GatewayAPIKeySource != "config" {
		t.Errorf("from file: %q (%q)", r.GatewayAPIKey, r.GatewayAPIKeySource)
	}
	t.Setenv("SALAD_AI_GATEWAY_API_KEY", "env-gw")
	r, _ = Resolve("", "")
	if r.GatewayAPIKey != "env-gw" || r.GatewayAPIKeySource != "env" {
		t.Errorf("env should win: %q (%q)", r.GatewayAPIKey, r.GatewayAPIKeySource)
	}
	// The two keys are independent.
	if r.APIKey != "" {
		t.Errorf("gateway key leaked into the API key: %q", r.APIKey)
	}
}

// Save replaces an existing config through a temp file and a rename: the
// content is replaced whole, the file stays 0600, and no temp file is left
// behind.
func TestSaveReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SALAD_CLI_CONFIG_DIR", dir)
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(
		p,
		[]byte(`{"api_key":"old-key","organization":"old-org","project":"a-much-longer-old-project-name"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	want := File{Organization: "new-org"}
	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	// A rename puts a new file in place; an in-place write keeps the old
	// one, and a crash partway through it would leave a torn file. Unix
	// only: on Windows os.SameFile reads the file IDs when it compares, by
	// path, so both stats name the current file.
	if after, err := os.Stat(p); runtime.GOOS != "windows" && (err != nil || os.SameFile(before, after)) {
		t.Errorf("Save rewrote the file in place (same file before and after); want a temp file renamed over it")
	}
	if got, err := Load(); err != nil || got != want {
		t.Errorf("Load after Save = %+v, %v; want %+v", got, err, want)
	}
	data, _ := os.ReadFile(p)
	if strings.Contains(string(data), "old") {
		t.Errorf("the old content survived: %s", data)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", info.Mode().Perm())
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".config-*.json")); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

// TestResolveSavedProjectBelongsToItsOrg: the config file's project is used
// only with the config file's organization; another organization from a
// flag or the environment gets the fallback, and the skipped project is
// reported. An explicit project still wins either way.
func TestResolveSavedProjectBelongsToItsOrg(t *testing.T) {
	clearEnv(t)
	t.Setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())
	if err := Save(File{Organization: "org-a", Project: "prod"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, orgFlag, orgEnv, projectFlag string
		wantProject, wantSource, wantSkip  string
	}{
		{"same org", "", "", "", "prod", "config", ""},
		{"same org named by flag", "org-a", "", "", "prod", "config", ""},
		{"other org by flag", "org-b", "", "", DefaultProject, SourceDefault, "prod"},
		{"other org by env", "", "org-b", "", DefaultProject, SourceDefault, "prod"},
		{"other org with an explicit project", "org-b", "", "staging", "staging", "flag", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SALAD_ORGANIZATION", tc.orgEnv)
			r, err := Resolve(tc.orgFlag, tc.projectFlag)
			if err != nil {
				t.Fatal(err)
			}
			if r.Project != tc.wantProject || r.ProjectSource != tc.wantSource || r.SkippedProject != tc.wantSkip {
				t.Errorf("project %q (%s), skipped %q; want %q (%s), skipped %q",
					r.Project, r.ProjectSource, r.SkippedProject, tc.wantProject, tc.wantSource, tc.wantSkip)
			}
			if tc.wantSkip != "" && r.SkippedProjectOrg != "org-a" {
				t.Errorf("SkippedProjectOrg = %q, want org-a", r.SkippedProjectOrg)
			}
		})
	}

	// A saved project with no saved organization still applies.
	if err := Save(File{Project: "prod"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SALAD_ORGANIZATION", "org-b")
	if r, _ := Resolve("", ""); r.Project != "prod" {
		t.Errorf("project = %q, want prod when the file names no organization", r.Project)
	}
}
