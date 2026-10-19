package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testEnv is an Env that reads only vars, with the config file in a fresh
// temp dir unless vars names one, so nothing on the machine running the
// tests leaks in. A test changes a variable by setting it in vars.
func testEnv(t *testing.T, vars map[string]string) Env {
	t.Helper()
	if _, ok := vars["SALAD_CLI_CONFIG_DIR"]; !ok {
		vars["SALAD_CLI_CONFIG_DIR"] = t.TempDir()
	}
	return Env{Getenv: func(k string) string { return vars[k] }, GOOS: runtime.GOOS}
}

// testEnvSaved is testEnv with file saved as its config file, unless file is empty.
func testEnvSaved(t *testing.T, vars map[string]string, file File) Env {
	t.Helper()
	env := testEnv(t, vars)
	if file == (File{}) {
		return env
	}
	if err := Save(env, file); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestPathHonorsConfigDirOverride(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := testEnv(t, map[string]string{"SALAD_CLI_CONFIG_DIR": dir})
	p, err := Path(env)
	if err != nil {
		t.Fatalf("Path(env) error: %v", err)
	}
	want := filepath.Join(dir, "config.json")
	if p != want {
		t.Errorf("Path(env) = %q, want %q", p, want)
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Parallel()
	env := testEnv(t, map[string]string{})
	f, err := Load(env)
	if err != nil {
		t.Fatalf("Load(env) on missing file: %v", err)
	}
	if f != (File{}) {
		t.Errorf("Load(env) = %+v, want zero File", f)
	}
}

func TestLoadCorruptFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := testEnv(t, map[string]string{"SALAD_CLI_CONFIG_DIR": dir})
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(env)
	if err == nil {
		t.Fatal("Load(env) on corrupt file: want error, got nil")
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
	t.Parallel()
	env := testEnv(t, map[string]string{"SALAD_CLI_CONFIG_DIR": filepath.Join(t.TempDir(), "nested")})
	want := File{APIKey: "salad_cloud_key", Organization: "my-org", Project: "my-project"}
	if err := Save(env, want); err != nil {
		t.Fatalf("Save(env) error: %v", err)
	}
	got, err := Load(env)
	if err != nil {
		t.Fatalf("Load(env) error: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestResolvePrecedence(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
			vars := map[string]string{}
			if tt.envOrg != "" {
				vars["SALAD_ORGANIZATION"] = tt.envOrg
			}
			env := testEnvSaved(t, vars, File{Organization: tt.fileOrg})
			r, err := Resolve(env, tt.flagOrg, "")
			if err != nil {
				t.Fatalf("Resolve(env) error: %v", err)
			}
			if r.Organization != tt.wantOrg || r.OrganizationSource != tt.wantOrgSrc {
				t.Errorf("Organization = %q (%q), want %q (%q)",
					r.Organization, r.OrganizationSource, tt.wantOrg, tt.wantOrgSrc)
			}
		})
	}
}

func TestResolveAllFields(t *testing.T) {
	t.Parallel()
	vars := map[string]string{}
	env := testEnv(t, vars)
	if err := Save(
		env,
		File{APIKey: "file-key", Organization: "file-org", Project: "file-project"},
	); err != nil {
		t.Fatal(err)
	}
	vars["SALAD_API_KEY"] = "env-key"
	vars["SALAD_PROJECT"] = "env-project"

	r, err := Resolve(env, "flag-org", "")
	if err != nil {
		t.Fatalf("Resolve(env) error: %v", err)
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
	t.Parallel()
	vars := map[string]string{}
	env := testEnv(t, vars)

	r, err := Resolve(env, "", "")
	if err != nil {
		t.Fatalf("Resolve(env) error: %v", err)
	}
	// Everything empty — except the project, which falls back to "default"
	// and says so in its source.
	want := Resolved{Project: DefaultProject, ProjectSource: SourceDefault}
	if r != want {
		t.Errorf("Resolve(env) = %+v, want %+v", r, want)
	}
}

// TestResolveProjectFallback: the "default" fallback is the lowest tier —
// any flag, env, or file value beats it, and the source names it.
func TestResolveProjectFallback(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                                 string
		envProject, flagProject, fileProject string
		want, wantSource                     string
	}{
		{"no project anywhere", "", "", "", "default", SourceDefault},
		{"env beats the fallback", "env-project", "", "", "env-project", "env"},
		{"flag beats everything", "env-project", "flag-project", "", "flag-project", "flag"},
		{"config beats the fallback", "", "", "file-project", "file-project", "config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := testEnvSaved(t, map[string]string{"SALAD_PROJECT": tc.envProject}, File{Project: tc.fileProject})
			r := must(Resolve(env, "", tc.flagProject))(t)
			if r.Project != tc.want || r.ProjectSource != tc.wantSource {
				t.Errorf("project = %q (%q), want %q (%q)", r.Project, r.ProjectSource, tc.want, tc.wantSource)
			}
			// The organization has no fallback.
			if r.Organization != "" || r.OrganizationSource != "" {
				t.Errorf("organization must not fall back: %q (%q)", r.Organization, r.OrganizationSource)
			}
		})
	}
}

// TestGatewayKey: the AI Gateway key is its own credential — round-trips
// through the file, honors the docs' env var name, env beats config.
func TestGatewayKey(t *testing.T) {
	t.Parallel()
	vars := map[string]string{}
	env := testEnv(t, vars)
	if err := Save(env, File{GatewayAPIKey: "file-gw"}); err != nil {
		t.Fatal(err)
	}
	r, err := Resolve(env, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.GatewayAPIKey != "file-gw" || r.GatewayAPIKeySource != "config" {
		t.Errorf("from file: %q (%q)", r.GatewayAPIKey, r.GatewayAPIKeySource)
	}
	vars["SALAD_AI_GATEWAY_API_KEY"] = "env-gw"
	r = must(Resolve(env, "", ""))(t)
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
	t.Parallel()
	dir := t.TempDir()
	env := testEnv(t, map[string]string{"SALAD_CLI_CONFIG_DIR": dir})
	p := filepath.Join(dir, "config.json")
	before := writeOldConfig(t, p)
	want := File{Organization: "new-org"}
	if err := Save(env, want); err != nil {
		t.Fatal(err)
	}
	// A rename puts a new file in place; an in-place write keeps the old
	// one, and a crash partway through it would leave a torn file. Unix
	// only: on Windows os.SameFile reads the file IDs when it compares, by
	// path, so both stats name the current file.
	if after, err := os.Stat(p); runtime.GOOS != "windows" && (err != nil || os.SameFile(before, after)) {
		t.Error("Save rewrote the file in place (same file before and after); want a temp file renamed over it")
	}
	if got, err := Load(env); err != nil || got != want {
		t.Errorf("Load after Save = %+v, %v; want %+v", got, err, want)
	}
	data := must(os.ReadFile(p))(t)
	if strings.Contains(string(data), "old") {
		t.Errorf("the old content survived: %s", data)
	}
	checkMode0600(t, p)
	if left := must(filepath.Glob(filepath.Join(dir, ".config-*.json")))(t); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

// writeOldConfig writes TestSaveReplacesAtomically's old config file at p and returns its file info.
func writeOldConfig(t *testing.T, p string) os.FileInfo {
	t.Helper()
	if err := os.WriteFile(
		p,
		[]byte(`{"api_key":"old-key","organization":"old-org","project":"a-much-longer-old-project-name"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	return must(os.Stat(p))(t)
}

// checkMode0600 checks that the file at p has mode 0600, on systems other than Windows, which has no such mode.
func checkMode0600(t *testing.T, p string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

// TestResolveSavedProjectBelongsToItsOrg: the config file's project is used
// only with the config file's organization; another organization from a
// flag or the environment gets the fallback, and the skipped project is
// reported. An explicit project still wins either way.
func TestResolveSavedProjectBelongsToItsOrg(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := Save(
		testEnv(t, map[string]string{"SALAD_CLI_CONFIG_DIR": dir}),
		File{Organization: "org-a", Project: "prod"},
	); err != nil {
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
			t.Parallel()
			env := testEnv(t, map[string]string{"SALAD_CLI_CONFIG_DIR": dir, "SALAD_ORGANIZATION": tc.orgEnv})
			r, err := Resolve(env, tc.orgFlag, tc.projectFlag)
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
}

// TestResolveSavedProjectWithoutOrg: a saved project with no saved
// organization still applies, whatever the organization.
func TestResolveSavedProjectWithoutOrg(t *testing.T) {
	t.Parallel()
	env := testEnv(t, map[string]string{"SALAD_ORGANIZATION": "org-b"})
	if err := Save(env, File{Project: "prod"}); err != nil {
		t.Fatal(err)
	}
	if r := must(Resolve(env, "", ""))(t); r.Project != "prod" {
		t.Errorf("project = %q, want prod when the file names no organization", r.Project)
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
