package cli

import (
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
)

// Keys for the warning tests, each with the mask it shows as.
const (
	keyABCD = "salad_cloud_user_EnvironmentKey00ABCD" // ****...ABCD
	key9QxZ = "salad_cloud_user_SavedKey000000009QxZ" // ****...9QxZ
)

// atATerminal puts the run at a terminal (or not) on os, in mode.
func atATerminal(t *testing.T, terminal bool, os, mode string) {
	t.Helper()
	oldCon, oldOS := con, goos
	con, goos = &fakeConsole{terminal: terminal}, os
	t.Setenv("CI", "")
	t.Setenv("SALAD_CLI_NO_PROMPT", "")
	if err := output.SetMode(mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { con, goos = oldCon, oldOS; _ = output.SetMode("table") })
}

func setEnv(t *testing.T, kv ...string) {
	t.Helper()
	for _, v := range []string{"SALAD_API_KEY", "SALAD_ORGANIZATION", "SALAD_PROJECT", "SALAD_AI_GATEWAY_API_KEY"} {
		t.Setenv(v, "")
	}
	for i := 0; i+1 < len(kv); i += 2 {
		t.Setenv(kv[i], kv[i+1])
	}
}

func warned(when overrideWhen, ovs []override) string {
	var b strings.Builder
	warnOverrides(&b, when, ovs)
	return b.String()
}

func TestOverrideWarnings(t *testing.T) {
	saved := config.File{APIKey: key9QxZ, Organization: "my-org", Project: "default"}
	for _, tc := range []struct {
		name     string
		terminal bool
		os, mode string
		env      []string
		when     overrideWhen
		vars     []string
		file     config.File
		want     string
	}{
		{
			name: "one variable, Windows, at a terminal", terminal: true, os: "windows", mode: "table",
			env: []string{"SALAD_API_KEY", keyABCD}, vars: []string{"SALAD_API_KEY"}, file: saved,
			want: `warning: commands will use SALAD_API_KEY (****...ABCD) instead of the API key you just saved (****...9QxZ). To remove the variable:
  PowerShell:  Remove-Item Env:SALAD_API_KEY
  cmd:         set SALAD_API_KEY=
If it comes back in a new terminal, remove it permanently:
  [Environment]::SetEnvironmentVariable('SALAD_API_KEY', $null, 'User')
Then open a new terminal (restart VS Code if you use its terminal).
`,
		},
		{
			name: "one variable, macOS, at a terminal", terminal: true, os: "darwin", mode: "table",
			env: []string{"SALAD_API_KEY", keyABCD}, vars: []string{"SALAD_API_KEY"}, file: saved,
			want: `warning: commands will use SALAD_API_KEY (****...ABCD) instead of the API key you just saved (****...9QxZ). To remove the variable:
  bash, zsh:  unset SALAD_API_KEY
  fish:       set -e SALAD_API_KEY
If it comes back in a new terminal, delete it from your shell profile (~/.zshrc, ~/.bashrc or similar).
`,
		},
		{
			name: "one variable, Linux, at a terminal", terminal: true, os: "linux", mode: "table",
			env: []string{"SALAD_API_KEY", keyABCD}, vars: []string{"SALAD_API_KEY"}, file: saved,
			want: `warning: commands will use SALAD_API_KEY (****...ABCD) instead of the API key you just saved (****...9QxZ). To remove the variable:
  bash, zsh:  unset SALAD_API_KEY
  fish:       set -e SALAD_API_KEY
If it comes back in a new terminal, delete it from your shell profile (~/.zshrc, ~/.bashrc or similar).
`,
		},
		{
			name: "one variable, no terminal", terminal: false, os: "windows", mode: "table",
			env: []string{"SALAD_API_KEY", keyABCD}, vars: []string{"SALAD_API_KEY"}, file: saved,
			want: "warning: commands will use SALAD_API_KEY (****...ABCD) instead of the API key you just saved (****...9QxZ)\n",
		},
		{
			name: "one variable, json mode at a terminal", terminal: true, os: "windows", mode: "json",
			env: []string{"SALAD_API_KEY", keyABCD}, vars: []string{"SALAD_API_KEY"}, file: saved,
			want: "warning: commands will use SALAD_API_KEY (****...ABCD) instead of the API key you just saved (****...9QxZ)\n",
		},
		{
			name: "two variables, Windows, at a terminal", terminal: true, os: "windows", mode: "table",
			env: []string{"SALAD_API_KEY", keyABCD, "SALAD_ORGANIZATION", "acme-prod"}, vars: []string{"SALAD_API_KEY", "SALAD_ORGANIZATION", "SALAD_PROJECT"}, file: saved,
			want: `warning: commands will use these environment variables instead of what you just saved:
warning:   SALAD_API_KEY       ****...ABCD  (saved: ****...9QxZ)
warning:   SALAD_ORGANIZATION  acme-prod    (saved: my-org)
To remove the variables:
  PowerShell:  Remove-Item Env:SALAD_API_KEY, Env:SALAD_ORGANIZATION
  cmd:         set "SALAD_API_KEY=" & set "SALAD_ORGANIZATION="
If they come back in a new terminal, remove them permanently:
  [Environment]::SetEnvironmentVariable('SALAD_API_KEY', $null, 'User')
  [Environment]::SetEnvironmentVariable('SALAD_ORGANIZATION', $null, 'User')
Then open a new terminal (restart VS Code if you use its terminal).
`,
		},
		{
			name: "two variables, Linux, at a terminal", terminal: true, os: "linux", mode: "table",
			env: []string{"SALAD_API_KEY", keyABCD, "SALAD_ORGANIZATION", "acme-prod"}, vars: []string{"SALAD_API_KEY", "SALAD_ORGANIZATION"}, file: saved,
			want: `warning: commands will use these environment variables instead of what you just saved:
warning:   SALAD_API_KEY       ****...ABCD  (saved: ****...9QxZ)
warning:   SALAD_ORGANIZATION  acme-prod    (saved: my-org)
To remove the variables:
  bash, zsh:  unset SALAD_API_KEY SALAD_ORGANIZATION
  fish:       set -e SALAD_API_KEY SALAD_ORGANIZATION
If they come back in a new terminal, delete them from your shell profile (~/.zshrc, ~/.bashrc or similar).
`,
		},
		{
			name: "two variables, no terminal", terminal: false, os: "linux", mode: "table",
			env: []string{"SALAD_API_KEY", keyABCD, "SALAD_ORGANIZATION", "acme-prod"}, vars: []string{"SALAD_API_KEY", "SALAD_ORGANIZATION"}, file: saved,
			want: `warning: commands will use these environment variables instead of what you just saved:
warning:   SALAD_API_KEY       ****...ABCD  (saved: ****...9QxZ)
warning:   SALAD_ORGANIZATION  acme-prod    (saved: my-org)
`,
		},
		{
			name: "two variables, json mode at a terminal", terminal: true, os: "windows", mode: "json",
			env: []string{"SALAD_API_KEY", keyABCD, "SALAD_ORGANIZATION", "acme-prod"}, vars: []string{"SALAD_API_KEY", "SALAD_ORGANIZATION"}, file: saved,
			want: `warning: commands will use these environment variables instead of what you just saved:
warning:   SALAD_API_KEY       ****...ABCD  (saved: ****...9QxZ)
warning:   SALAD_ORGANIZATION  acme-prod    (saved: my-org)
`,
		},
		{
			name: "use: the organization", terminal: true, os: "windows", mode: "table",
			env: []string{"SALAD_ORGANIZATION", "my-org"}, vars: []string{"SALAD_ORGANIZATION", "SALAD_PROJECT"},
			file: config.File{Organization: "acme-prod", Project: "default"},
			want: `warning: commands will use SALAD_ORGANIZATION (my-org) instead of the organization you just saved (acme-prod). To remove the variable:
  PowerShell:  Remove-Item Env:SALAD_ORGANIZATION
  cmd:         set SALAD_ORGANIZATION=
If it comes back in a new terminal, remove it permanently:
  [Environment]::SetEnvironmentVariable('SALAD_ORGANIZATION', $null, 'User')
Then open a new terminal (restart VS Code if you use its terminal).
`,
		},
		{
			name: "a value cleared while a variable supplies one", terminal: false, os: "linux", mode: "table",
			env: []string{"SALAD_PROJECT", "staging"}, vars: []string{"SALAD_PROJECT"}, file: config.File{Organization: "acme-prod"},
			want: "warning: commands still use SALAD_PROJECT (staging)\n",
		},
		{
			name: "doctor", terminal: true, os: "windows", mode: "table", when: asSaved,
			env: []string{"SALAD_API_KEY", keyABCD}, vars: []string{"SALAD_API_KEY"}, file: saved,
			want: `warning: commands use SALAD_API_KEY (****...ABCD) instead of the saved API key (****...9QxZ). To remove the variable:
  PowerShell:  Remove-Item Env:SALAD_API_KEY
  cmd:         set SALAD_API_KEY=
If it comes back in a new terminal, remove it permanently:
  [Environment]::SetEnvironmentVariable('SALAD_API_KEY', $null, 'User')
Then open a new terminal (restart VS Code if you use its terminal).
`,
		},
		{
			name: "no variable differs", terminal: true, os: "windows", mode: "table",
			env: []string{"SALAD_API_KEY", key9QxZ}, vars: []string{"SALAD_API_KEY", "SALAD_ORGANIZATION"}, file: saved,
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			atATerminal(t, tc.terminal, tc.os, tc.mode)
			setEnv(t, tc.env...)
			if got := warned(tc.when, overridesOf(tc.file, tc.vars...)); got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestRemovalWarnings(t *testing.T) {
	for _, tc := range []struct {
		name, os, mode string
		terminal       bool
		env            []string
		vars           []string
		want           string
	}{
		{
			"logout, Windows, at a terminal", "windows", "table", true,
			[]string{"SALAD_API_KEY", keyABCD},
			[]string{"SALAD_API_KEY"},
			`warning: commands still use SALAD_API_KEY (****...ABCD). To remove the variable:
  PowerShell:  Remove-Item Env:SALAD_API_KEY
  cmd:         set SALAD_API_KEY=
If it comes back in a new terminal, remove it permanently:
  [Environment]::SetEnvironmentVariable('SALAD_API_KEY', $null, 'User')
Then open a new terminal (restart VS Code if you use its terminal).
`,
		},
		{
			"config unset, no terminal", "linux", "table", false,
			[]string{"SALAD_ORGANIZATION", "acme-prod"},
			[]string{"SALAD_ORGANIZATION"},
			"warning: commands still use SALAD_ORGANIZATION (acme-prod)\n",
		},
		{
			"logout, json mode", "darwin", "json", true,
			[]string{"SALAD_API_KEY", keyABCD},
			[]string{"SALAD_API_KEY"},
			"warning: commands still use SALAD_API_KEY (****...ABCD)\n",
		},
		{"logout, nothing set", "windows", "table", true, nil, []string{"SALAD_API_KEY"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			atATerminal(t, tc.terminal, tc.os, tc.mode)
			setEnv(t, tc.env...)
			ovs := stillSupplied(tc.vars...)
			if got := warned(afterRemove, ovs); got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
			if names := overrideNames(ovs); names == nil || len(names) != len(tc.env)/2 {
				t.Errorf("env_overrides = %#v", names)
			}
		})
	}
}

// TestNoPromptAndCIDropTheSteps: SALAD_CLI_NO_PROMPT and CI make the warning
// the line alone, as no terminal does.
func TestNoPromptAndCIDropTheSteps(t *testing.T) {
	for _, v := range []string{"SALAD_CLI_NO_PROMPT", "CI"} {
		atATerminal(t, true, "windows", "table")
		setEnv(t, "SALAD_API_KEY", keyABCD)
		t.Setenv(v, "1")
		if got := warned(
			afterRemove,
			stillSupplied("SALAD_API_KEY"),
		); got != "warning: commands still use SALAD_API_KEY (****...ABCD)\n" {
			t.Errorf("%s=1: got %q", v, got)
		}
	}
}

func TestBeforePrompt(t *testing.T) {
	setEnv(
		t,
		"SALAD_API_KEY",
		keyABCD,
		"SALAD_ORGANIZATION",
		"acme-prod",
		"SALAD_AI_GATEWAY_API_KEY",
		"sk-gw-0000000000EFGH",
	)
	empty := config.File{}
	for _, tc := range []struct {
		command string
		vars    []string
		want    string
	}{
		{"login", []string{"SALAD_API_KEY"}, "SALAD_API_KEY is set in this shell (****...ABCD); commands will use it instead of what login saves."},
		{"login", []string{"SALAD_API_KEY", "SALAD_ORGANIZATION"}, "SALAD_API_KEY (****...ABCD) and SALAD_ORGANIZATION (acme-prod) are set in this shell; commands will use them instead of what login saves."},
		{"ai-gateway login", []string{"SALAD_AI_GATEWAY_API_KEY"}, "SALAD_AI_GATEWAY_API_KEY is set in this shell (****...EFGH); commands will use it instead of what ai-gateway login saves."},
		{"login", []string{"SALAD_PROJECT"}, ""},
	} {
		if got := beforePrompt(tc.command, overridesOf(empty, tc.vars...)); got != tc.want {
			t.Errorf("%s %v:\n got %q\nwant %q", tc.command, tc.vars, got, tc.want)
		}
	}
	three := overridesOf(empty, "SALAD_API_KEY", "SALAD_ORGANIZATION", "SALAD_AI_GATEWAY_API_KEY")
	if got := beforePrompt(
		"login",
		three,
	); !strings.HasPrefix(
		got,
		"SALAD_API_KEY (****...ABCD), SALAD_ORGANIZATION (acme-prod) and SALAD_AI_GATEWAY_API_KEY (****...EFGH) are set",
	) {
		t.Errorf("three variables: %q", got)
	}
}

func TestOverrideNames(t *testing.T) {
	setEnv(t, "SALAD_ORGANIZATION", "my-org")
	if got := overrideNames(
		overridesOf(config.File{Organization: "my-org"}, "SALAD_ORGANIZATION"),
	); got == nil ||
		len(got) != 0 {
		t.Errorf("equal values: %#v, want []", got)
	}
	if got := overrideNames(
		overridesOf(config.File{Organization: "acme-prod"}, "SALAD_ORGANIZATION", "SALAD_API_KEY"),
	); len(got) != 1 ||
		got[0] != "SALAD_ORGANIZATION" {
		t.Errorf("one differing: %#v", got)
	}
}
