//go:build live

// The setup commands against the real API: login, use, whoami, doctor,
// logout and ai-gateway login, and the run-time errors every command shares
// (a rejected key, an invalid name). Every case here runs the binary with
// its own empty config folder (SALAD_CLI_CONFIG_DIR) and without the
// inherited SALAD_API_KEY, SALAD_ORGANIZATION, SALAD_PROJECT and
// SALAD_AI_GATEWAY_API_KEY, so the user's config file is never read or
// written and each case sets exactly the variables it is about. Stdin is
// never a terminal here: these are the scripted paths. The prompts are
// covered offline, through the injected console.
//
// Nothing here spends money except TestSetupGatewayLogin, which sends one
// billed completion and runs only when SALAD_AI_GATEWAY_API_KEY is set.
package livetest

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// setupVars are never inherited by a setup case; each case sets the ones
// it is about.
func setupVars() []string {
	return []string{
		"SALAD_API_KEY",
		"SALAD_ORGANIZATION",
		"SALAD_PROJECT",
		"SALAD_AI_GATEWAY_API_KEY",
		"SALAD_CLI_CONFIG_DIR",
	}
}

// apiKey is the real API key this run holds, from SALAD_API_KEY.
func apiKey() string { return os.Getenv("SALAD_API_KEY") }

// badKey is a well-formed key the API rejects; checking it costs nothing.
func badKey() string { return "lt-not-a-key-" + prefix }

// liveContext is the organization and project the setup cases log in to:
// SALAD_ORGANIZATION, and SALAD_PROJECT or "default", the project every
// organization starts with.
func liveContext() (org, project string) {
	project = os.Getenv("SALAD_PROJECT")
	if project == "" {
		project = "default"
	}
	return os.Getenv("SALAD_ORGANIZATION"), project
}

// mask is how the CLI shows a key: ****... and its last four.
func mask(k string) string {
	r := []rune(k)
	if len(r) <= 8 {
		return "****"
	}
	return "****..." + string(r[len(r)-4:])
}

// cfgDir is a setup case's config folder.
type cfgDir string

func newCfg(t *testing.T) cfgDir {
	t.Helper()
	return cfgDir(t.TempDir())
}

// path is the config file the CLI reports for this folder.
func (d cfgDir) path() string { return filepath.Join(string(d), "config.json") }

// run invokes the CLI with this config folder, the variables in env (none
// of setupVars is inherited) and stdin ("" leaves it empty). A real key in
// the output fails the case and is replaced, so no failure message prints
// one.
func (d cfgDir) run(t *testing.T, env map[string]string, stdin string, args ...string) result {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binPath(t), args...)
	cmd.Env = d.environ(env)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running %v: %v", args, err)
	}
	with := ""
	if names := slices.Sorted(maps.Keys(env)); len(names) > 0 {
		with = " (with " + strings.Join(names, ", ") + ")"
	}
	t.Logf("$ saladcloud %s%s -> exit %d", strings.Join(args, " "), with, code)
	return scrubKeys(t, args, result{Code: code, Stdout: out.String(), Stderr: errb.String()})
}

// environ is a setup case's environment: the inherited one without setupVars, SALAD_CLI_CONFIG_DIR set to this
// folder, and the variables in env.
func (d cfgDir) environ(env map[string]string) []string {
	var environ []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.ContainsFunc(setupVars(), func(v string) bool { return strings.EqualFold(v, name) }) {
			environ = append(environ, kv)
		}
	}
	environ = append(environ, "SALAD_CLI_CONFIG_DIR="+string(d))
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	return environ
}

// scrubKeys fails the case when r, the result of args, holds the value of SALAD_API_KEY or
// SALAD_AI_GATEWAY_API_KEY, and returns it with each such value replaced by the variable's name.
func scrubKeys(t *testing.T, args []string, r result) result {
	t.Helper()
	for _, name := range []string{"SALAD_API_KEY", "SALAD_AI_GATEWAY_API_KEY"} {
		if v := os.Getenv(name); v != "" && strings.Contains(r.Stdout+r.Stderr, v) {
			t.Errorf("%v printed the value of %s", args, name)
			r.Stdout = strings.ReplaceAll(r.Stdout, v, "<"+name+">")
			r.Stderr = strings.ReplaceAll(r.Stderr, v, "<"+name+">")
		}
	}
	return r
}

// loggedIn is a config folder holding the key, organization and project,
// saved by a scripted login.
func loggedIn(t *testing.T) cfgDir {
	t.Helper()
	d := newCfg(t)
	org, project := liveContext()
	if r := d.run(t, nil, apiKey()+"\n", "login", "--api-key", "-", "--org", org, "--project", project); r.Code != 0 {
		t.Fatalf("login exit %d: %s", r.Code, r.Stderr)
	}
	return d
}

// whoami is whoami's document for this folder.
func (d cfgDir) whoami(t *testing.T, env map[string]string) map[string]any {
	t.Helper()
	r := d.run(t, env, "", "whoami", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("whoami exit %d: %s", r.Code, r.Stderr)
	}
	return oneDoc(t, r)
}

// expect checks doc's keys against want: strings, bools, nil, and
// []any{} for an empty array.
func expect(t *testing.T, what string, doc, want map[string]any) {
	t.Helper()
	for k, v := range want {
		got, ok := doc[k]
		if !ok {
			t.Errorf("%s: no key %s", what, k)
		} else if !reflect.DeepEqual(got, v) {
			t.Errorf("%s: %s = %#v, want %#v", what, k, got, v)
		}
	}
}

// same fails unless got equals want, showing both.
func same(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n%s\nwant:\n%s", what, got, want)
	}
}

// errorDoc is a failed run's error document: the last line of stderr,
// after nothing but warning: and note: lines, with stdout empty.
func errorDoc(t *testing.T, r result) map[string]any {
	t.Helper()
	if strings.TrimSpace(r.Stdout) != "" {
		t.Errorf("stdout on error: %s", r.Stdout)
	}
	lines := strings.Split(strings.TrimRight(r.Stderr, "\n"), "\n")
	for _, l := range lines[:len(lines)-1] {
		if !strings.HasPrefix(l, "warning: ") && !strings.HasPrefix(l, "note: ") {
			t.Errorf("stderr line before the error document: %s", l)
		}
	}
	var doc struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &doc); err != nil || doc.Error == nil {
		t.Fatalf("stderr does not end in an error document: %s", r.Stderr)
	}
	return doc.Error
}

// machineStderr fails on a json-mode stderr line other than warning: or
// note:.
func machineStderr(t *testing.T, r result) {
	t.Helper()
	for l := range strings.SplitSeq(strings.TrimRight(r.Stderr, "\n"), "\n") {
		if l != "" && !strings.HasPrefix(l, "warning: ") && !strings.HasPrefix(l, "note: ") {
			t.Errorf("json-mode stderr carries %q", l)
		}
	}
}

// TestSetupLogin: the two scripted logins, a piped key with --org and
// --project, and the variables alone; and a key the API rejects.
func TestSetupLogin(t *testing.T) {
	t.Parallel()
	org, project := liveContext()
	k := apiKey()

	t.Run("a piped key with --org and --project", func(t *testing.T) {
		t.Parallel()
		d := newCfg(t)
		r := d.run(t, nil, k+"\n", "login", "--api-key", "-", "--org", org, "--project", project)
		if r.Code != 0 {
			t.Fatalf("exit %d: %s", r.Code, r.Stderr)
		}
		same(t, "stdout", r.Stdout, fmt.Sprintf("Logged in: %s/%s\nConfig at: %s\n", org, project, d.path()))
		same(
			t,
			"stderr",
			r.Stderr,
			fmt.Sprintf(
				"API key: %s\nOrganization: %s (from --org)\nProject: %s (from --project)\n",
				mask(k),
				org,
				project,
			),
		)
		expect(t, "whoami after login", d.whoami(t, nil), map[string]any{
			"saved_api_key": mask(k), "saved_organization": org, "saved_project": project,
			"api_key_source": "config", "authenticated": true, "organization_found": true,
		})
	})

	t.Run("a piped key, -o json", func(t *testing.T) {
		t.Parallel()
		d := newCfg(t)
		r := d.run(t, nil, k+"\n", "login", "--api-key", "-", "--org", org, "--project", project, "-o", "json")
		if r.Code != 0 {
			t.Fatalf("exit %d: %s", r.Code, r.Stderr)
		}
		same(t, "stderr", r.Stderr, "")
		expect(t, "login", oneDoc(t, r), map[string]any{
			"organization": org, "project": project, "api_key": mask(k), "api_key_source": "config",
			"api_key_saved": true, "env_overrides": []any{}, "config_path": d.path(), "action": "logged_in", "ok": true,
		})
	})

	// The variables alone: the key is checked and not saved, the
	// organization and project are saved.
	env := map[string]string{"SALAD_API_KEY": k, "SALAD_ORGANIZATION": org}
	projectLine := "Project: default\n"
	if p := os.Getenv("SALAD_PROJECT"); p != "" {
		env["SALAD_PROJECT"] = p
		projectLine = fmt.Sprintf("Project: %s (from SALAD_PROJECT)\n", p)
	}
	t.Run("the variables alone", func(t *testing.T) {
		t.Parallel()
		d := newCfg(t)
		r := d.run(t, env, "", "login")
		if r.Code != 0 {
			t.Fatalf("exit %d: %s", r.Code, r.Stderr)
		}
		same(t, "stdout", r.Stdout, fmt.Sprintf("Logged in: %s/%s\nConfig at: %s\n", org, project, d.path()))
		same(
			t,
			"stderr",
			r.Stderr,
			fmt.Sprintf(
				"No API key provided, using SALAD_API_KEY (%s).\nOrganization: %s (from SALAD_ORGANIZATION)\n",
				mask(k),
				org,
			)+
				projectLine+"The API key is not saved, so it works only where SALAD_API_KEY is set.\nhint: to save "+
				"it, pipe it "+
				"to saladcloud login --api-key -\n",
		)
		expect(t, "whoami after login", d.whoami(t, nil), map[string]any{
			"saved_api_key": nil, "saved_organization": org, "saved_project": project,
		})
	})
	t.Run("the variables alone, -o json", func(t *testing.T) {
		t.Parallel()
		d := newCfg(t)
		r := d.run(t, env, "", "login", "-o", "json")
		if r.Code != 0 {
			t.Fatalf("exit %d: %s", r.Code, r.Stderr)
		}
		same(t, "stderr", r.Stderr, "")
		expect(t, "login", oneDoc(t, r), map[string]any{
			"organization": org, "project": project, "api_key": mask(k), "api_key_source": "env",
			"api_key_saved": false, "env_overrides": []any{}, "action": "logged_in", "ok": true,
		})
	})

	t.Run("a rejected key", func(t *testing.T) {
		t.Parallel()
		d := newCfg(t)
		r := d.run(t, nil, badKey()+"\n", "login", "--api-key", "-", "--org", org, "-o", "json")
		if r.Code != 3 {
			t.Errorf("exit %d, want 3: %s", r.Code, r.Stderr)
		}
		expect(t, "error", errorDoc(t, r), map[string]any{
			"exit_code": float64(3), "kind": "auth", "http_status": float64(401),
			"message": fmt.Sprintf("API key %s (from stdin) is invalid", mask(badKey())),
			"hint":    "check that you copied all of it, and that it isn't an AI Gateway key",
		})
		expect(t, "whoami after a failed login", d.whoami(t, nil), map[string]any{
			"saved_api_key": nil, "saved_organization": nil, "saved_project": nil,
		})
	})

	// A named organization or project that does not exist fails, and
	// nothing is saved.
	missing := prefix + "-nope"
	for _, tc := range []struct {
		name string
		args []string
		want map[string]any
	}{
		{"an organization that does not exist", []string{"--org", missing}, map[string]any{
			"message": fmt.Sprintf("organization \"%s\" (from --org) not found", missing),
			"hint":    "to list yours, run saladcloud orgs list",
		}},
		{"a project that does not exist", []string{"--org", org, "--project", missing}, map[string]any{
			"message": fmt.Sprintf("project \"%s\" (from --project) not found in %s", missing, org),
			"hint":    fmt.Sprintf("create one at https://portal.salad.com/organizations/%s/projects/new", org),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newCfg(t)
			r := d.run(t, nil, k+"\n", append([]string{"login", "--api-key", "-", "-o", "json"}, tc.args...)...)
			if r.Code != 4 {
				t.Errorf("exit %d, want 4: %s", r.Code, r.Stderr)
			}
			tc.want["kind"], tc.want["http_status"] = "not_found", float64(404)
			expect(t, "error", errorDoc(t, r), tc.want)
			expect(
				t,
				"whoami after a failed login",
				d.whoami(t, nil),
				map[string]any{"saved_api_key": nil, "saved_organization": nil},
			)
		})
	}
}

// TestSetupUse switches the saved context with the saved key, and checks
// that a miss changes nothing.
func TestSetupUse(t *testing.T) {
	t.Parallel()
	org, project := liveContext()
	d := loggedIn(t)

	// use <org> takes "default" when the organization has it, and sets no
	// project when it does not.
	r := d.run(t, nil, "", "use", org)
	if r.Code != 0 {
		t.Fatalf("use %s exit %d: %s", org, r.Code, r.Stderr)
	}
	hasDefault := r.Stdout == fmt.Sprintf("Switched to %s/default\n", org)
	if hasDefault {
		same(
			t,
			"use <org> stderr",
			r.Stderr,
			fmt.Sprintf(
				"hint: no project named, so \"default\" is used; for another, run saladcloud use %s/<project>\n",
				org,
			),
		)
	} else {
		same(t, "use <org> stdout", r.Stdout, fmt.Sprintf("Switched to %s, no project set\n", org))
		same(
			t,
			"use <org> stderr",
			r.Stderr,
			fmt.Sprintf("Project \"default\" not found in %s. Create one later if needed.\n", org),
		)
	}
	r = d.run(t, nil, "", "use", org, "-o", "json")
	if r.Code != 0 {
		t.Fatalf("use %s -o json exit %d: %s", org, r.Code, r.Stderr)
	}
	same(t, "use <org> -o json stderr", r.Stderr, "")
	var wantProject any
	if hasDefault {
		wantProject = "default"
	}
	expect(t, "use <org>", oneDoc(t, r), map[string]any{
		"organization": org, "project": wantProject, "env_overrides": []any{}, "action": "switched", "ok": true,
	})

	r = d.run(t, nil, "", "use", org+"/"+project)
	if r.Code != 0 {
		t.Fatalf("use %s/%s exit %d: %s", org, project, r.Code, r.Stderr)
	}
	same(t, "use <org>/<project> stdout", r.Stdout, fmt.Sprintf("Switched to %s/%s\n", org, project))
	same(t, "use <org>/<project> stderr", r.Stderr, "")

	// A variable that overrides what use saved: the one-line warning (no
	// terminal) and env_overrides name it.
	other := prefix + "-envproj"
	r = d.run(t, map[string]string{"SALAD_PROJECT": other}, "", "use", org+"/"+project, "-o", "json")
	if r.Code != 0 {
		t.Fatalf("use with SALAD_PROJECT set exit %d: %s", r.Code, r.Stderr)
	}
	same(
		t,
		"use with SALAD_PROJECT set, stderr",
		r.Stderr,
		fmt.Sprintf(
			"warning: commands will use SALAD_PROJECT (%s) instead of the project you just saved (%s)\n",
			other,
			project,
		),
	)
	expect(t, "use with SALAD_PROJECT set", oneDoc(t, r), map[string]any{
		"organization": org, "project": project, "env_overrides": []any{"SALAD_PROJECT"},
	})

	// Misses change nothing.
	missing := prefix + "-nope"
	r = d.run(t, nil, "", "use", org+"/"+missing, "-o", "json")
	if r.Code != 4 {
		t.Errorf("use of a missing project exit %d, want 4: %s", r.Code, r.Stderr)
	}
	expect(t, "use of a missing project", errorDoc(t, r), map[string]any{
		"kind": "not_found", "http_status": float64(404),
		"message": fmt.Sprintf("project \"%s\" not found in %s", missing, org),
		"hint":    fmt.Sprintf("create one at https://portal.salad.com/organizations/%s/projects/new", org),
	})
	r = d.run(t, nil, "", "use", missing)
	if r.Code != 4 {
		t.Errorf("use of a missing organization exit %d, want 4: %s", r.Code, r.Stderr)
	}
	same(
		t,
		"use of a missing organization, stderr",
		r.Stderr,
		fmt.Sprintf(
			"saladcloud: error: organization \"%s\" not found\nhint: to list yours, run saladcloud orgs list\n",
			missing,
		),
	)
	expect(
		t,
		"whoami after the misses",
		d.whoami(t, nil),
		map[string]any{"saved_organization": org, "saved_project": project},
	)
}

// TestSetupWhoami reports the saved context, and a rejected key without
// failing.
func TestSetupWhoami(t *testing.T) {
	t.Parallel()
	org, project := liveContext()
	k := apiKey()
	d := loggedIn(t)
	doc := d.whoami(t, nil)
	expect(t, "whoami", doc, map[string]any{
		"api_key": mask(k), "api_key_source": "config", "saved_api_key": mask(k),
		"organization": org, "organization_source": "config", "saved_organization": org, "organization_found": true,
		"project": project, "project_source": "config", "saved_project": project,
		"ai_gateway_api_key": nil, "saved_ai_gateway_api_key": nil,
		"authenticated": true, "config_path": d.path(),
	})
	if orgs := valueAs[[]any](doc["organizations"]); !slices.Contains(orgs, any(org)) {
		t.Errorf("whoami organizations = %v, want it to include %s", doc["organizations"], org)
	}

	// A rejected key from the variable, hiding the saved one: reported, not
	// failed.
	bad := map[string]string{"SALAD_API_KEY": badKey()}
	expect(t, "whoami with a rejected key", d.whoami(t, bad), map[string]any{
		"api_key": mask(badKey()), "api_key_source": "env", "saved_api_key": mask(k), "authenticated": false,
	})
	r := d.run(t, bad, "", "whoami")
	if r.Code != 0 {
		t.Fatalf("whoami table exit %d: %s", r.Code, r.Stderr)
	}
	if want := fmt.Sprintf(
		"%s (from SALAD_API_KEY; saved: %s)",
		mask(badKey()),
		mask(k),
	); !strings.Contains(
		r.Stdout,
		want,
	) {
		t.Errorf("whoami does not show %q:\n%s", want, r.Stdout)
	}
}

// TestSetupDoctor passes on a fresh login, and names the failed check with
// a rejected key.
func TestSetupDoctor(t *testing.T) {
	t.Parallel()
	k := apiKey()
	d := loggedIn(t)
	r := d.run(t, nil, "", "doctor", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("doctor exit %d: %s", r.Code, r.Stderr)
	}
	machineStderr(t, r)
	doctorPassRows(t, doctorRows(t, r))
	doctorTableHeader(t, d)
	doctorRejectedKey(t, d, k)
}

// doctorPassRows checks doctor's rows on a fresh login: every check in order, each with a hint key, no passing row
// with a hint, and the checks a working login satisfies passing.
func doctorPassRows(t *testing.T, rows []map[string]any) {
	t.Helper()
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, assertAs[string](t, row["name"]))
		if _, ok := row["hint"]; !ok {
			t.Errorf("doctor row %v has no hint key", row["name"])
		}
		if row["status"] == "pass" && row["hint"] != nil {
			t.Errorf("doctor row %v passes with a hint: %v", row["name"], row["hint"])
		}
	}
	same(
		t,
		"doctor rows",
		strings.Join(names, " "),
		"config api-reachable api-key organization project ai-gateway-api-key-present local-ssh-key-present",
	)
	pass := []string{"config", "api-key", "organization", "project"}
	if os.Getenv("SALAD_CLI_API_BASE_URL") == "" {
		pass = append(pass, "api-reachable")
	}
	for _, row := range rows {
		if slices.Contains(pass, assertAs[string](t, row["name"])) && row["status"] != "pass" {
			t.Errorf("doctor %v = %v (%v), want pass", row["name"], row["status"], row["detail"])
		}
	}
}

// doctorTableHeader checks the header of doctor's table for the config folder d.
func doctorTableHeader(t *testing.T, d cfgDir) {
	t.Helper()
	table := d.run(t, nil, "", "doctor")
	if table.Code != 0 {
		t.Fatalf("doctor table exit %d: %s", table.Code, table.Stderr)
	}
	first, _, _ := strings.Cut(
		table.Stdout,
		"\n",
	)
	if strings.Join(
		strings.Fields(first),
		" ",
	) != "CHECK STATUS DETAIL" {
		t.Errorf("doctor table header = %q", first)
	}
}

// doctorRejectedKey runs doctor in d with a rejected key from the variable: api-key fails, the checks that need it
// skip, and the run names the failure and warns that the variable hides the saved key k.
func doctorRejectedKey(t *testing.T, d cfgDir, k string) {
	t.Helper()
	r := d.run(t, map[string]string{"SALAD_API_KEY": badKey()}, "", "doctor", "-o", "json")
	if r.Code != 1 {
		t.Errorf("doctor with a rejected key exit %d, want 1: %s", r.Code, r.Stderr)
	}
	for _, row := range doctorRows(t, r) {
		switch row["name"] {
		case "api-key":
			expect(t, "doctor api-key row", row, map[string]any{
				"status": "fail",
				"detail": mask(badKey()) + " (from SALAD_API_KEY) rejected",
				"hint": fmt.Sprintf(
					"remove SALAD_API_KEY to use the saved API key (%s), or set it to a working key",
					mask(k),
				),
			})
		case "organization", "project":
			if row["status"] != "skip" {
				t.Errorf("doctor %v = %v with a rejected key, want skip", row["name"], row["status"])
			}
		}
	}
	r.Stdout = "" // the rows above; errorDoc wants it empty
	expect(t, "doctor's error", errorDoc(t, r), map[string]any{
		"kind": "api_error", "message": "1 check failed: api-key", "hint": "each failing row's hint says how to fix it",
	})
	if !strings.Contains(
		r.Stderr,
		fmt.Sprintf(
			"warning: commands use SALAD_API_KEY (%s) instead of the saved API key (%s)",
			mask(badKey()),
			mask(k),
		),
	) {
		t.Errorf("doctor does not warn about the variable hiding the saved key:\n%s", r.Stderr)
	}
}

// doctorRows is doctor's items.
func doctorRows(t *testing.T, r result) []map[string]any {
	t.Helper()
	var doc struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &doc); err != nil {
		t.Fatalf("doctor stdout is not one document: %v\n%s", err, r.Stdout)
	}
	return doc.Items
}

// TestSetupLogout removes the key and keeps the context, and warns about
// a variable that still supplies a key.
func TestSetupLogout(t *testing.T) {
	t.Parallel()
	org, project := liveContext()
	k := apiKey()
	d := loggedIn(t)
	r := d.run(t, nil, "", "logout")
	if r.Code != 0 {
		t.Fatalf("logout exit %d: %s", r.Code, r.Stderr)
	}
	same(
		t,
		"logout stdout",
		r.Stdout,
		fmt.Sprintf(
			"Logged out: removed the saved API key (%s)\nOrganization and project stay saved: %s/%s\n",
			mask(k),
			org,
			project,
		),
	)
	same(t, "logout stderr", r.Stderr, "")
	expect(t, "whoami after logout", d.whoami(t, nil), map[string]any{
		"saved_api_key": nil, "saved_organization": org, "saved_project": project,
	})
	r = d.run(t, nil, "", "logout", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("second logout exit %d: %s", r.Code, r.Stderr)
	}
	expect(t, "second logout", oneDoc(t, r), map[string]any{
		"removed":       false,
		"organization":  org,
		"project":       project,
		"env_overrides": []any{},
		"action":        "logged_out",
		"ok":            true,
	})

	d = loggedIn(t)
	env := map[string]string{"SALAD_API_KEY": k}
	r = d.run(t, env, "", "logout", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("logout with SALAD_API_KEY set exit %d: %s", r.Code, r.Stderr)
	}
	same(
		t,
		"logout with SALAD_API_KEY set, stderr",
		r.Stderr,
		fmt.Sprintf("warning: commands still use SALAD_API_KEY (%s)\n", mask(k)),
	)
	expect(t, "logout with SALAD_API_KEY set", oneDoc(t, r), map[string]any{
		"removed": true, "organization": org, "project": project, "env_overrides": []any{"SALAD_API_KEY"},
	})
}

// TestRunTimeRejectedKey: a key the API rejects, from each source, is
// named masked with its source and the fix there. Nothing is created.
func TestRunTimeRejectedKey(t *testing.T) {
	t.Parallel()
	org, _ := liveContext()
	k := apiKey()
	rejected := fmt.Sprintf("API key %s (from %%s) was rejected", mask(badKey()))

	d := newCfg(t)
	r := d.run(
		t,
		map[string]string{"SALAD_API_KEY": badKey(), "SALAD_ORGANIZATION": org},
		"",
		"quotas",
		"get",
		"-o",
		"json",
	)
	if r.Code != 3 {
		t.Errorf("SALAD_API_KEY, none saved: exit %d, want 3", r.Code)
	}
	expect(t, "SALAD_API_KEY, none saved", errorDoc(t, r), map[string]any{
		"exit_code": float64(3), "kind": "auth", "http_status": float64(401),
		"message": fmt.Sprintf(rejected, "SALAD_API_KEY"),
		"hint":    "set SALAD_API_KEY to a working key, or remove it and run saladcloud login",
	})

	d = loggedIn(t)
	r = d.run(t, map[string]string{"SALAD_API_KEY": badKey()}, "", "quotas", "get")
	if r.Code != 3 {
		t.Errorf("SALAD_API_KEY, another key saved: exit %d, want 3", r.Code)
	}
	same(
		t,
		"SALAD_API_KEY, another key saved, stderr",
		r.Stderr,
		"saladcloud: error: "+fmt.Sprintf(rejected, "SALAD_API_KEY")+"\n"+
			fmt.Sprintf(
				"hint: remove SALAD_API_KEY to use the saved API key (%s), or set it to a working key\n",
				mask(k),
			),
	)

	d = newCfg(t)
	if r = d.run(t, nil, badKey()+"\n", "config", "set", "api-key", "-"); r.Code != 0 {
		t.Fatalf("config set api-key - exit %d: %s", r.Code, r.Stderr)
	}
	r = d.run(t, map[string]string{"SALAD_ORGANIZATION": org}, "", "quotas", "get", "-o", "json")
	expect(t, "from config", errorDoc(t, r), map[string]any{
		"exit_code": float64(3), "kind": "auth", "http_status": float64(401),
		"message": fmt.Sprintf(rejected, "config"), "hint": "run saladcloud login to save a new key",
	})
}

// TestRunTimeMissingOrganization: a 404 under an organization the key
// cannot see is reported as that organization, after exactly one extra
// request (the organization check), or none when the request that failed
// was the check itself. --debug prints one line per request.
func TestRunTimeMissingOrganization(t *testing.T) {
	t.Parallel()
	d := loggedIn(t)
	missing := prefix + "-nope"
	want := fmt.Sprintf(
		"saladcloud: error: organization \"%s\" (from --org) not found\nhint: to list yours, run saladcloud orgs "+
			"list\n",
		missing,
	)
	for _, tc := range []struct {
		args     []string
		requests int
	}{
		{[]string{"container-groups", "list", "--org", missing, "--project", "default"}, 2},
		{[]string{"quotas", "get", "--org", missing}, 1},
	} {
		r := d.run(t, nil, "", append(tc.args, "--debug")...)
		if r.Code != 4 {
			t.Errorf("%v: exit %d, want 4", tc.args, r.Code)
		}
		var rest strings.Builder
		requests := 0
		for _, l := range strings.SplitAfter(r.Stderr, "\n") {
			if strings.HasPrefix(l, "debug: ") {
				requests++
			} else {
				rest.WriteString(l)
			}
		}
		same(t, fmt.Sprintf("%v stderr without debug lines", tc.args), rest.String(), want)
		if requests != tc.requests {
			t.Errorf("%v made %d requests, want %d:\n%s", tc.args, requests, tc.requests, r.Stderr)
		}
	}
	r := d.run(t, nil, "", "container-groups", "list", "--org", missing, "--project", "default", "-o", "json")
	expect(t, "-o json", errorDoc(t, r), map[string]any{
		"exit_code":   float64(4),
		"kind":        "not_found",
		"http_status": float64(404),
		"message": fmt.Sprintf(
			"organization \"%s\" (from --org) not found",
			missing,
		),
		"hint": "to list yours, run saladcloud orgs list",
	})
}

// TestRunTimeInvalidName: an organization or project name that breaks the
// API's pattern fails before any request. --debug prints a line for every
// request, which the control shows.
func TestRunTimeInvalidName(t *testing.T) {
	t.Parallel()
	d := loggedIn(t)
	if r := d.run(t, nil, "", "quotas", "get", "--debug"); r.Code != 0 || !strings.Contains(r.Stderr, "debug: GET ") {
		t.Fatalf("control: quotas get --debug exit %d, no request line in: %s", r.Code, r.Stderr)
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{
			"--org with capitals", nil,
			[]string{"quotas", "get", "--org", "My-Org"},
			"saladcloud: error: invalid organization name \"My-Org\" (from --org)\nhint: did you mean \"my-org\"?\n",
		},
		{
			"SALAD_ORGANIZATION with a space",
			map[string]string{"SALAD_ORGANIZATION": "Acme Prod"},
			[]string{"quotas", "get"},
			"saladcloud: error: invalid organization name \"Acme Prod\" (from SALAD_ORGANIZATION)\n" +
				"hint: it looks like a display name; use the name from the portal URL, the part after " +
				"/organizations/\n",
		},
		{
			"--project with capitals", nil,
			[]string{"container-groups", "list", "--project", "STAGING"},
			"saladcloud: error: invalid project name \"STAGING\" (from --project)\nhint: did you mean \"staging\"?\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := d.run(t, tc.env, "", append(tc.args, "--debug")...)
			if r.Code != 6 {
				t.Errorf("exit %d, want 6", r.Code)
			}
			same(t, "stderr (no debug: line means no request)", r.Stderr, tc.want)
		})
	}
	r := d.run(t, nil, "", "quotas", "get", "--org", "My-Org", "-o", "json")
	expect(t, "-o json", errorDoc(t, r), map[string]any{
		"exit_code": float64(6), "kind": "invalid_input", "http_status": nil,
		"message": "invalid organization name \"My-Org\" (from --org)", "hint": "did you mean \"my-org\"?",
	})
}

// TestSetupGatewayLogin: with only SALAD_AI_GATEWAY_API_KEY and no
// terminal, ai-gateway login checks the key (one billed completion) and
// saves nothing.
func TestSetupGatewayLogin(t *testing.T) {
	t.Parallel()
	gw := os.Getenv("SALAD_AI_GATEWAY_API_KEY")
	if gw == "" {
		t.Skip("SALAD_AI_GATEWAY_API_KEY unset; skipping ai-gateway login (one billed request)")
	}
	d := newCfg(t)
	env := map[string]string{"SALAD_AI_GATEWAY_API_KEY": gw}
	r := d.run(t, env, "", "ai-gateway", "login", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	machineStderr(t, r)
	doc := oneDoc(t, r)
	expect(t, "ai-gateway login", doc, map[string]any{
		"ai_gateway_api_key": mask(gw), "ai_gateway_api_key_source": "env", "ai_gateway_api_key_saved": false,
		"env_overrides": []any{}, "config_path": d.path(), "action": "logged_in", "ok": true,
	})
	if n := valueAs[float64](doc["models"]); n < 1 {
		t.Errorf("models = %v, want at least one", doc["models"])
	}
	if c, ok := doc["credits_available"].(bool); !ok {
		t.Errorf("credits_available = %v, want a bool", doc["credits_available"])
	} else if !c {
		t.Log("NOTE: the key's organization is out of credits; the key was still accepted")
	}
	expect(t, "whoami after ai-gateway login", d.whoami(t, env), map[string]any{
		"ai_gateway_api_key": mask(gw), "ai_gateway_api_key_source": "env", "saved_ai_gateway_api_key": nil,
	})
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

// valueAs returns v as a T, or T's zero value when it holds something else
// (a field missing from a decoded document).
func valueAs[T any](v any) T {
	if x, ok := v.(T); ok {
		return x
	}
	var zero T
	return zero
}
