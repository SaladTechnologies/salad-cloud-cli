package cli

// Environment beats config, out loud: a variable outranks the config file,
// so a command that writes the file names every variable that will be used
// instead of what it wrote, or that still supplies a value it removed. At a
// terminal, in table mode, the warning carries the commands that remove the
// variable for the user's OS; otherwise it is the warning alone, and the
// JSON document's env_overrides carries the same names.

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
)

// envSetting is a value the environment can supply over the config file.
type envSetting struct {
	env    string // the variable
	what   string // how a message names the value
	secret bool   // shown masked
	saved  func(config.File) string
}

func envSettings() []envSetting {
	return []envSetting{
		{"SALAD_API_KEY", labelAPIKey, true, func(f config.File) string { return f.APIKey }},
		{"SALAD_ORGANIZATION", nameKindOrganization, false, func(f config.File) string { return f.Organization }},
		{"SALAD_PROJECT", nameKindProject, false, func(f config.File) string { return f.Project }},
		{"SALAD_AI_GATEWAY_API_KEY", labelGatewayKey, true, func(f config.File) string { return f.GatewayAPIKey }},
	}
}

// show renders a value the way messages show it: keys masked, and a name
// that looks like a key hidden.
func (s envSetting) show(app *App, v string) string {
	if s.secret {
		return maskAPIKey(v)
	}
	return app.shownName(strings.TrimSpace(v))
}

// used is the value as commands use it: a key normalized, a name trimmed,
// so a stray carriage return does not read as a different value.
func (s envSetting) used(v string) string {
	if s.secret {
		key, _ := normalizeKey(v)
		return key
	}
	return strings.TrimSpace(v)
}

// override is a variable that commands use instead of what the config file
// holds, or holds no longer.
type override struct {
	setting envSetting
	value   string // the variable's
	saved   string // the config file's; "" when there is none
}

// overridesOf lists the variables among vars (in envSettings' order) that
// are set and differ from what f holds: once a command has saved f, these
// are what commands will use instead.
func (app *App) overridesOf(f config.File, vars ...string) []override {
	var out []override
	for _, s := range envSettings() {
		if !hasString(vars, s.env) {
			continue
		}
		if v := app.Env.Getenv(s.env); v != "" && s.used(v) != s.used(s.saved(f)) {
			out = append(out, override{s, v, s.saved(f)})
		}
	}
	return out
}

// stillSupplied lists the variables among vars that are set at all: once a
// command has removed their values from the config file, commands still use
// them.
func (app *App) stillSupplied(vars ...string) []override {
	var out []override
	for _, s := range envSettings() {
		if !hasString(vars, s.env) {
			continue
		}
		if v := app.Env.Getenv(s.env); v != "" {
			out = append(out, override{setting: s, value: v, saved: ""})
		}
	}
	return out
}

// overrideNames is a document's env_overrides: the variables the warning
// names, [] when none.
func overrideNames(ovs []override) []string {
	names := make([]string, 0, len(ovs))
	for _, o := range ovs {
		names = append(names, o.setting.env)
	}
	return names
}

// The three things a variable warning can say.
type overrideWhen int

const (
	afterSave   overrideWhen = iota // commands will use it instead of what was just saved
	afterRemove                     // commands still use it after its value was removed
	asSaved                         // commands use it instead of what is saved (doctor)
)

// warnOverrides writes the warning for ovs to w, one warning: line per
// row; at a terminal in table mode it is followed by the commands that
// remove the variables on this OS. It writes nothing for none.
func (app *App) warnOverrides(w io.Writer, when overrideWhen, ovs []override) {
	if len(ovs) == 0 {
		return
	}
	steps := !app.printer.Machine() && app.atTerminal()
	if len(ovs) == 1 {
		line := app.overrideLine(when, &ovs[0])
		if !steps {
			fprintln(w, line)
			return
		}
		fprintln(w, line+". To remove the variable:")
		fprint(w, app.removalSteps(ovs))
		return
	}
	fprintln(w, overridesHeading(when))
	app.writeOverrideRows(w, when, ovs)
	if steps {
		fprintln(w, "To remove the variables:")
		fprint(w, app.removalSteps(ovs))
	}
}

// overrideLine is the warning for a single variable, on one line.
func (app *App) overrideLine(when overrideWhen, o *override) string {
	switch {
	case when == afterRemove || o.saved == "" && when == afterSave:
		return fmt.Sprintf("warning: commands still use %s (%s)", o.setting.env, o.setting.show(app, o.value))
	case when == asSaved:
		return fmt.Sprintf(
			"warning: commands use %s (%s) instead of the saved %s (%s)",
			o.setting.env,
			o.setting.show(app, o.value),
			o.setting.what,
			o.setting.show(app, o.saved),
		)
	}
	return fmt.Sprintf(
		"warning: commands will use %s (%s) instead of the %s you just saved (%s)",
		o.setting.env,
		o.setting.show(app, o.value),
		o.setting.what,
		o.setting.show(app, o.saved),
	)
}

// overridesHeading is the first line of the warning for several variables.
func overridesHeading(when overrideWhen) string {
	switch when {
	case afterRemove:
		return "warning: commands still use these environment variables:"
	case asSaved:
		return "warning: commands use these environment variables instead of what is saved:"
	case afterSave: // the wording below
	}
	return "warning: commands will use these environment variables instead of what you just saved:"
}

// writeOverrideRows writes one aligned warning: line per variable, with the saved value it hides unless the value
// was just removed.
func (app *App) writeOverrideRows(w io.Writer, when overrideWhen, ovs []override) {
	nameWidth, valueWidth := 0, 0
	for _, o := range ovs {
		nameWidth = max(nameWidth, len(o.setting.env))
		valueWidth = max(valueWidth, len(o.setting.show(app, o.value)))
	}
	for _, o := range ovs {
		row := fmt.Sprintf("warning:   %-*s  %-*s", nameWidth, o.setting.env, valueWidth, o.setting.show(app, o.value))
		switch {
		case when == afterRemove:
		case o.saved == "":
			row += "  (none saved)"
		default:
			row += "  (saved: " + o.setting.show(app, o.saved) + ")"
		}
		fprintln(w, strings.TrimRight(row, " "))
	}
}

// removalSteps are the lines that remove the variables on this OS: from the
// current shell, then for good.
func (app *App) removalSteps(ovs []override) string {
	names := overrideNames(ovs)
	one := len(names) == 1
	it, comes := "it", "it comes"
	if !one {
		it, comes = "them", "they come"
	}
	var b strings.Builder
	if app.Env.GOOS == setupGOOSWindows {
		envs := make([]string, len(names))
		sets := make([]string, len(names))
		for i, n := range names {
			envs[i] = "Env:" + n
			sets[i] = `set "` + n + `="`
		}
		cmd := strings.Join(sets, " & ")
		if one {
			cmd = "set " + names[0] + "="
		}
		fprintf(&b, "  PowerShell:  Remove-Item %s\n", strings.Join(envs, ", "))
		fprintf(&b, "  cmd:         %s\n", cmd)
		fprintf(&b, "If %s back in a new terminal, remove %s permanently:\n", comes, it)
		for _, n := range names {
			fprintf(&b, "  [Environment]::SetEnvironmentVariable('%s', $null, 'User')\n", n)
		}
		b.WriteString("Then open a new terminal (restart VS Code if you use its terminal).\n")
		return b.String()
	}
	fprintf(&b, "  bash, zsh:  unset %s\n", strings.Join(names, " "))
	fprintf(&b, "  fish:       set -e %s\n", strings.Join(names, " "))
	fprintf(
		&b,
		"If %s back in a new terminal, delete %s from your shell profile (~/.zshrc, ~/.bashrc or similar).\n",
		comes,
		it,
	)
	return b.String()
}

// beforePrompt is the line a login prints at a terminal before its first
// prompt, naming the set variables that would override what it saves;
// command is "login" or "ai-gateway login". "" for none.
func (app *App) beforePrompt(command string, ovs []override) string {
	switch len(ovs) {
	case 0:
		return ""
	case 1:
		o := ovs[0]
		return fmt.Sprintf(
			"%s is set in this shell (%s); commands will use it instead of what %s saves.",
			o.setting.env,
			o.setting.show(app, o.value),
			command,
		)
	}
	parts := make([]string, len(ovs))
	for i, o := range ovs {
		parts[i] = fmt.Sprintf("%s (%s)", o.setting.env, o.setting.show(app, o.value))
	}
	list := strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	return fmt.Sprintf("%s are set in this shell; commands will use them instead of what %s saves.", list, command)
}

// hasString reports whether list holds s.
func hasString(list []string, s string) bool {
	return slices.Contains(list, s)
}
