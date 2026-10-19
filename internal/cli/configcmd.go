package cli

import (
	"bufio"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
)

const configKeys = "api-key, organization, project, ai-gateway-api-key"

// isSecretKey reports whether a config key holds a credential that must be
// masked on output.
func isSecretKey(key string) bool { return key == "api-key" || key == "ai-gateway-api-key" }

func orNotSet(v string) string {
	if v == "" {
		return "(not set)"
	}
	return v
}

// strOrNil maps "" to nil so unset strings render as JSON null while the
// key stays present — the CLI's stable-JSON-shape convention.
func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// maskedOrNil is strOrNil for API keys: null when unset, masked otherwise.
func maskedOrNil(k string) *string {
	if k == "" {
		return nil
	}
	m := maskAPIKey(k)
	return &m
}

// configEntry is one config key and its value, as config get prints it:
// value is null when unset, and the two API keys are masked when set.
type configEntry struct {
	Key   string  `json:"key"`
	Value *string `json:"value"`
}

// configListResult is config list's JSON contract: the list envelope, one
// entry per config key in configKeys order, and the file's path beside it.
type configListResult struct {
	Items      []configEntry `json:"items"`
	ConfigPath string        `json:"config_path"`
}

// configSetResult and configUnsetResult are config set's and unset's JSON
// contracts: the key written, plus the action shape every state-changing
// command carries.
type configSetResult struct {
	Key          string   `json:"key"`
	Value        string   `json:"value"`
	EnvOverrides []string `json:"env_overrides"`
	ConfigPath   string   `json:"config_path"`
	Action       string   `json:"action"`
	OK           bool     `json:"ok"`
}

type configUnsetResult struct {
	Key          string   `json:"key"`
	EnvOverrides []string `json:"env_overrides"`
	ConfigPath   string   `json:"config_path"`
	Action       string   `json:"action"`
	OK           bool     `json:"ok"`
}

// configKeyEnv is the variable that supplies each config key's value
// instead of the config file.
func configKeyEnv() map[string]string {
	return map[string]string{
		"api-key":            envAPIKey,
		"organization":       envOrganization,
		"project":            envProject,
		"ai-gateway-api-key": envGatewayKey,
	}
}

// loadConfigFor reads the config file for a config command, with the fix
// when it cannot be read.
func (app *App) loadConfigFor(command string) (config.File, string, *clierr.Error) {
	path, err := config.Path(app.Env)
	if err != nil {
		return config.File{}, "", clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot find a folder for the config file: %s", config.Reason(err)).
			WithHint("set SALAD_CLI_CONFIG_DIR to a writable folder")
	}
	f, err := config.Load(app.Env)
	if err != nil {
		return config.File{}, path, clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot read the config file %s: %s", path, readReason(err)).
			WithHint("fix or delete it, then run saladcloud %s again", command)
	}
	return f, path, nil
}

// saveConfigFor writes the config file for a config command.
func (app *App) saveConfigFor(command, path string, f config.File) *clierr.Error {
	if err := config.Save(app.Env, f); err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot write the config file %s: %s", path, config.Reason(err)).
			WithHint("set SALAD_CLI_CONFIG_DIR to a writable folder, then run saladcloud %s again", command)
	}
	return nil
}

// configGetResult is config get's JSON contract; value is null when the key
// is unset, as in config list.
type configGetResult struct {
	Key   string  `json:"key"`
	Value *string `json:"value"`
}

// configPathResult is config path's JSON contract.
type configPathResult struct {
	ConfigPath string `json:"config_path"`
}

// configListOf builds config list's entries from a config file.
func (app *App) configListOf(f config.File, path string) configListResult {
	return configListResult{
		Items: []configEntry{
			{"api-key", maskedOrNil(f.APIKey)},
			{"organization", app.nameOrNil(f.Organization)},
			{"project", app.nameOrNil(f.Project)},
			{"ai-gateway-api-key", maskedOrNil(f.GatewayAPIKey)},
		},
		ConfigPath: path,
	}
}

func getField(f *config.File, key string) (*string, *clierr.Error) {
	switch key {
	case "api-key":
		return &f.APIKey, nil
	case "organization":
		return &f.Organization, nil
	case "project":
		return &f.Project, nil
	case "ai-gateway-api-key":
		return &f.GatewayAPIKey, nil
	default:
		return nil, clierr.New(clierr.CodeUsage, clierr.KindUsage, `unknown config key "%s"`, key).
			WithHint("the keys are api-key, organization, project and ai-gateway-api-key")
	}
}

// addConfigCommands adds configcmd.go's commands to the tree.
func (app *App) addConfigCommands(root *cobra.Command) {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Manage the saladcloud config file",
	}

	setCmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value (keys: " + configKeys + ")",
		Long: `Set one config value as given: no check against the API. To switch the
organization and project together, checked, use saladcloud use <org>[/<project>].

For api-key and ai-gateway-api-key, a value of - reads the key from the first
line of stdin, so it is not exposed on the command line:

    # bash or zsh
    printf '%s' "$KEY" | saladcloud config set api-key -

    # PowerShell
    $env:KEY | saladcloud config set api-key -

A variable (SALAD_API_KEY, SALAD_ORGANIZATION, SALAD_PROJECT,
SALAD_AI_GATEWAY_API_KEY) that is set is used instead of the saved value;
config set says so.`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error { return app.runConfigSet(args[0], args[1]) },
	}

	unsetCmd := &cobra.Command{
		Use:   "unset <key>",
		Short: "Remove a config value (keys: " + configKeys + ")",
		Long: `Remove one value from the config file. To start over entirely, delete the
file that saladcloud config path prints. A variable that still supplies the
value (SALAD_API_KEY, SALAD_ORGANIZATION, SALAD_PROJECT,
SALAD_AI_GATEWAY_API_KEY) is still used; config unset says so.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error { return app.runConfigUnset(args[0]) },
	}

	getCmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Print a config value (keys: " + configKeys + "; the two keys print masked)",
		Long: `Print one value from the config file. The API key and the AI Gateway key
print masked to their last four characters (****...ABCD), never whole.
Keys: ` + configKeys + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error { return app.runConfigGet(args[0]) },
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "Print all config values (both keys masked) and the config path",
		Args:  cobra.NoArgs,
		RunE:  func(_ *cobra.Command, _ []string) error { return app.runConfigList() },
	}

	pathCmd := &cobra.Command{
		Use:   "path",
		Short: "Print the config file path",
		Args:  cobra.NoArgs,
		RunE:  func(_ *cobra.Command, _ []string) error { return app.runConfigPath() },
	}

	configCmd.AddCommand(setCmd, unsetCmd, getCmd, listCmd, pathCmd)
	root.AddCommand(configCmd)
}

// runConfigSet is config set: the value as given (or from stdin for a key), written without a check against the
// API, then reported.
func (app *App) runConfigSet(key, value string) error {
	value, cerr := app.configSetValue(key, value)
	if cerr != nil {
		return cerr
	}
	f, path, cerr := app.loadConfigFor("config set")
	if cerr != nil {
		return cerr
	}
	field, cerr := getField(&f, key)
	if cerr != nil {
		return cerr
	}
	// A saved project belongs to the saved organization; config set
	// writes as given, so changing the organization alone carries the
	// project over. Say so, and name the command that switches both.
	var carriedFrom *string
	if key == "organization" && f.Project != "" && value != f.Organization {
		oldOrg := f.Organization
		carriedFrom = &oldOrg
	}
	*field = value
	if cerr := app.saveConfigFor("config set", path, f); cerr != nil {
		return cerr
	}
	return app.emitConfigSet(key, value, path, f, carriedFrom)
}

// configSetValue is the value config set writes: the argument, or for a key, the first line of stdin when the
// argument is -. A key given as an argument draws a warning; a key given as an organization or project is refused.
func (app *App) configSetValue(key, value string) (string, *clierr.Error) {
	if isSecretKey(key) && value != "-" {
		fprintf(
			app.errw(),
			"warning: a key passed as an argument is exposed on the command line; pipe it to saladcloud config set %s - instead\n",
			key,
		)
	}
	if value == "-" && isSecretKey(key) {
		line, cerr := app.configSetKeyFromStdin(key)
		if cerr != nil {
			return "", cerr
		}
		value = line
	}
	if (key == "organization" || key == "project") && app.looksLikeKey(value) {
		return "", clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "that looks like an API key, not %s %s name", article(key), key).
			WithHint("to save an API key, pipe it to saladcloud config set api-key -")
	}
	return value, nil
}

// configSetKeyFromStdin reads config set's key from the first line of stdin. It is hidden whenever stdin is a
// terminal, as the logins read --api-key -: CI or SALAD_CLI_NO_PROMPT turn prompts off, not the terminal's echo.
func (app *App) configSetKeyFromStdin(key string) (string, *clierr.Error) {
	var line string
	var err error
	if app.Console.Terminal() {
		if line, err = app.Console.ReadHidden(); err != nil {
			return "", clierr.New(
				clierr.CodeGeneric,
				clierr.KindInterrupted,
				"config set canceled, nothing changed",
			)
		}
	} else {
		line, err = readLine(bufio.NewReader(app.Stdin))
	}
	if err != nil || line == "" {
		return "", clierr.New(clierr.CodeUsage, clierr.KindUsage, "no %s value on stdin", key).
			WithHint("pipe the key in")
	}
	return line, nil
}

// emitConfigSet prints config set's result (a key masked), then the warning for a project carried over to a new
// organization (carriedFrom is the old one, nil when none was carried) and the variables used instead.
func (app *App) emitConfigSet(key, value, path string, f config.File, carriedFrom *string) error {
	shown := value
	if isSecretKey(key) {
		shown = maskAPIKey(value)
	}
	ovs := app.overridesOf(f, configKeyEnv()[key])
	result := configSetResult{key, shown, overrideNames(ovs), path, "set", true}
	if err := app.printer.Emit(result, func(w *output.Writer) {
		w.Printf("Set %s to %s\n", key, shown)
		w.Printf("Config at: %s\n", path)
	}); err != nil {
		return wrapErr(err)
	}
	if carriedFrom != nil {
		fprintf(
			app.errw(),
			"warning: the saved project \"%s\" was saved with %s and now applies to %s\n",
			app.shownName(f.Project),
			app.shownName(*carriedFrom),
			value,
		)
		app.hintLine("to switch both, run saladcloud use %s/<project>", value)
	}
	app.warnOverrides(app.errw(), afterSave, ovs)
	return nil
}

// runConfigUnset is config unset: the key removed from the file, and the variable that still supplies it named.
func (app *App) runConfigUnset(key string) error {
	f, path, cerr := app.loadConfigFor("config unset")
	if cerr != nil {
		return cerr
	}
	field, cerr := getField(&f, key)
	if cerr != nil {
		return cerr
	}
	*field = ""
	if cerr := app.saveConfigFor("config unset", path, f); cerr != nil {
		return cerr
	}
	ovs := app.stillSupplied(configKeyEnv()[key])
	result := configUnsetResult{key, overrideNames(ovs), path, "unset", true}
	if err := app.printer.Emit(result, func(w *output.Writer) {
		w.Printf("Unset %s\n", key)
		w.Printf("Config at: %s\n", path)
	}); err != nil {
		return wrapErr(err)
	}
	app.warnOverrides(app.errw(), afterRemove, ovs)
	return nil
}

// runConfigGet is config get: one value from the file, a key masked.
func (app *App) runConfigGet(key string) error {
	f, _, cerr := app.loadConfigFor("config get")
	if cerr != nil {
		return cerr
	}
	field, cerr := getField(&f, key)
	if cerr != nil {
		return cerr
	}
	value := strOrNil(*field)
	if isSecretKey(key) {
		value = maskedOrNil(*field)
	}
	result := configGetResult{key, value}
	return wrapErr(app.printer.Emit(result, func(w *output.Writer) {
		if value == nil {
			w.Println("(not set)")
			return
		}
		w.Println(*value)
	}))
}

// runConfigList is config list: every value in the file, both keys masked, and the file's path.
func (app *App) runConfigList() error {
	f, path, cerr := app.loadConfigFor("config list")
	if cerr != nil {
		return cerr
	}
	result := app.configListOf(f, path)
	return wrapErr(app.printer.Emit(result, func(w *output.Writer) {
		w.Printf("api-key\t%s\n", maskAPIKey(f.APIKey))
		w.Printf("organization\t%s\n", orNotSet(app.shownName(f.Organization)))
		w.Printf("project\t%s\n", orNotSet(app.shownName(f.Project)))
		w.Printf("ai-gateway-api-key\t%s\n", maskAPIKey(f.GatewayAPIKey))
		w.Printf("config-path\t%s\n", path)
	}))
}

// runConfigPath is config path: the config file's path.
func (app *App) runConfigPath() error {
	path, err := config.Path(app.Env)
	if err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot find a folder for the config file: %s", config.Reason(err)).
			WithHint("set SALAD_CLI_CONFIG_DIR to a writable folder")
	}
	result := configPathResult{path}
	return wrapErr(app.printer.Emit(result, func(w *output.Writer) {
		w.Println(path)
	}))
}
