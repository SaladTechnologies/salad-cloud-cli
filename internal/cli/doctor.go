package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
)

// Check statuses.
const (
	checkPass = "pass"
	checkFail = "fail"
	checkWarn = "warn"
	checkSkip = "skip"
)

// checkResult is one row of doctor output: what was found, and the fix
// (null under a PASS row, and where there is nothing to do).
type checkResult struct {
	Name   string  `json:"name"`
	Status string  `json:"status"`
	Detail string  `json:"detail"`
	Hint   *string `json:"hint"`
}

// doctorResult is doctor's document: the rows, and the variables that hide
// a different saved value (the warning after the table names the same).
type doctorResult struct {
	Items        []checkResult `json:"items"`
	EnvOverrides []string      `json:"env_overrides"`
}

// apiHost is how doctor names the production API; Deps.ProductionAPI is where
// it probes it (tests point it at a stand-in).
const apiHost = "https://api.salad.com"

// doctorProbeTimeout is how long the api-reachable row waits for any answer.
const doctorProbeTimeout = 10 * time.Second

// addDoctorCommands adds doctor.go's commands to the tree.
func (app *App) addDoctorCommands(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the CLI's configuration and connectivity",
		Long: `Check, in order: the config file, that the API answers, the API key, the
organization, the project, whether an AI Gateway key is set, and whether a
local SSH key exists. Each row says what it found; a row that is not PASS is
followed by a hint saying what to do. A check that needs an earlier one
(the API key needs the API, the project a working organization) is SKIP and
says what it could not check.

doctor never spends: it does not test the AI Gateway key (saladcloud
ai-gateway test does, with one billed request), and it cannot see whether
your SSH key is registered (register it at https://portal.salad.com/ssh-keys).
A variable that overrides a saved value is named after the table.

Exits 0 when no row failed, and 1 naming the failed checks otherwise.`,
		Args: cobra.NoArgs,
		RunE: app.runDoctor,
	}
	root.AddCommand(cmd)
}

// doctorRun is one run's findings, which later checks read.
type doctorRun struct {
	orgFlag, projectFlag  string
	saved                 config.File
	res                   config.Resolved
	reachable             bool
	keyOK                 bool
	orgs                  []string      // the key's organizations; nil: unknown
	orgChecked            *clierr.Error // the organization check, once made
	orgCheckMade, orgGood bool
	app                   *App // the run
}

func (app *App) runDoctor(cmd *cobra.Command, _ []string) error {
	orgFlag, projectFlag := app.flags.org, app.flags.project
	d := &doctorRun{
		orgFlag:     orgFlag,
		projectFlag: projectFlag,
		saved:       config.File{APIKey: "", Organization: "", Project: "", GatewayAPIKey: ""},
		res: config.Resolved{
			APIKey:              "",
			APIKeySource:        "",
			Organization:        "",
			OrganizationSource:  "",
			Project:             "",
			ProjectSource:       "",
			GatewayAPIKey:       "",
			GatewayAPIKeySource: "",
			SkippedProject:      "",
			SkippedProjectOrg:   "",
		},
		reachable:    false,
		keyOK:        false,
		orgs:         nil,
		orgChecked:   nil,
		orgCheckMade: false,
		orgGood:      false,
		app:          app,
	}
	ctx := cmd.Context()
	rows := []checkResult{
		d.configRow(),
		d.reachableRow(ctx),
		d.apiKeyRow(ctx),
		d.organizationRow(ctx),
		d.projectRow(ctx),
		d.gatewayRow(),
		d.sshRow(ctx),
	}
	ovs := d.hidingOverrides()
	err := app.printer.Emit(doctorResult{Items: rows, EnvOverrides: overrideNames(ovs)}, func(w *output.Writer) {
		w.Println("CHECK\tSTATUS\tDETAIL")
		for _, r := range rows {
			w.Printf("%s\t%s\t%s\n", r.Name, strings.ToUpper(r.Status), r.Detail)
		}
	})
	if err != nil {
		return wrapErr(err)
	}
	for _, r := range rows {
		if r.Hint != nil {
			app.hintLine("%s: %s", r.Name, *r.Hint)
		}
	}
	app.warnOverrides(app.errw(), asSaved, ovs)
	return app.doctorFailure(rows)
}

// hidingOverrides is the variables that hide a different saved value: the warning after the table, and
// env_overrides beside the rows.
func (d *doctorRun) hidingOverrides() []override {
	var ovs []override
	for _, o := range d.app.overridesOf(d.saved, envAPIKey, envOrganization, envProject, envGatewayKey) {
		if o.saved != "" {
			ovs = append(ovs, o)
		}
	}
	return ovs
}

// doctorFailure is doctor's error when a row failed, naming the failed checks; nil when none did.
func (app *App) doctorFailure(rows []checkResult) error {
	var failed []string
	for _, r := range rows {
		if r.Status == checkFail {
			failed = append(failed, r.Name)
		}
	}
	if len(failed) == 0 {
		return nil
	}
	checks := "checks"
	if len(failed) == 1 {
		checks = "check"
	}
	hint := "each failing row's hint above says how to fix it"
	if app.printer.Machine() {
		hint = "each failing row's hint says how to fix it"
	}
	return clierr.New(
		clierr.CodeGeneric,
		clierr.KindAPIError,
		"%d %s failed: %s",
		len(failed),
		checks,
		strings.Join(failed, ", "),
	).WithHint("%s", hint)
}

func row(name, status, detail, hint string) checkResult {
	r := checkResult{Name: name, Status: status, Detail: detail, Hint: nil}
	if hint != "" {
		r.Hint = &hint
	}
	return r
}

// configRow checks the config file; the run's settings resolve from it, or
// from flags and the environment alone when it cannot be read.
func (d *doctorRun) configRow() checkResult {
	app := d.app
	path, err := config.Path(app.Env)
	if err != nil {
		d.res = config.ResolveFrom(app.Env,
			config.File{APIKey: "", Organization: "", Project: "", GatewayAPIKey: ""},
			d.orgFlag,
			d.projectFlag,
		)
		return row("config", checkFail, "no config location: "+config.Reason(err),
			"set SALAD_CLI_CONFIG_DIR, or use SALAD_API_KEY, SALAD_ORGANIZATION and SALAD_PROJECT")
	}
	f, err := config.Load(app.Env)
	if err != nil {
		d.res = config.ResolveFrom(app.Env,
			config.File{APIKey: "", Organization: "", Project: "", GatewayAPIKey: ""},
			d.orgFlag,
			d.projectFlag,
		)
		return row(
			"config",
			checkFail,
			fmt.Sprintf("%s cannot be read: %s", path, readReason(err)),
			"fix or delete it, then run saladcloud login again",
		)
	}
	d.saved = f
	d.res = config.ResolveFrom(app.Env, f, d.orgFlag, d.projectFlag)
	if err := config.Writable(app.Env); err != nil {
		return row(
			"config",
			checkWarn,
			path+" is read-only; login and use cannot save",
			"set SALAD_CLI_CONFIG_DIR to a writable folder",
		)
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return row("config", checkPass, "none yet at "+path+"; using flags and environment", "")
	}
	return row("config", checkPass, path, "")
}

// reachableRow checks the API answers at all (any HTTP status will do).
func (d *doctorRun) reachableRow(ctx context.Context) checkResult {
	app := d.app
	base, shown, override := app.ProductionAPI, apiHost, app.Env.Getenv("SALAD_CLI_API_BASE_URL")
	if override != "" {
		base, shown = override, override
	}
	probeCtx, cancel := context.WithTimeout(ctx, doctorProbeTimeout)
	defer cancel()
	if _, err := api.Probe(probeCtx, base+"/organizations/doctor-probe/gpu-classes", app.apiOptions()); err != nil {
		var verr *tls.CertificateVerificationError
		if errors.As(err, &verr) && len(verr.UnverifiedCertificates) > 0 &&
			time.Now().Before(verr.UnverifiedCertificates[0].NotBefore) {
			return row("api-reachable", checkFail, fmt.Sprintf(
				"%s: its TLS certificate is not valid until %s",
				shown,
				verr.UnverifiedCertificates[0].NotBefore.UTC().
					Format(time.RFC3339),
			), "check this machine's date and time")
		}
		return row(
			"api-reachable",
			checkFail,
			fmt.Sprintf("%s: %v", shown, errors.Unwrap(err)),
			"check your network or proxy settings",
		)
	}
	d.reachable = true
	if override != "" {
		return row("api-reachable", checkWarn, override+" (from SALAD_CLI_API_BASE_URL)",
			"every command uses this server; remove SALAD_CLI_API_BASE_URL to use "+apiHost)
	}
	return row("api-reachable", checkPass, shown, "")
}

// keyShown is the key as a row shows it: masked, with its source.
func (d *doctorRun) keyShown() string {
	return maskAPIKey(d.res.APIKey) + fromTag(originOf(d.res.APIKeySource, "", envAPIKey))
}

// unchecked is a row's detail when the API could not answer the check.
func unchecked(cerr *clierr.Error) string {
	if cerr.HTTPStatus != nil {
		return fmt.Sprintf("could not be checked: the API answered %d", *cerr.HTTPStatus)
	}
	return "could not be checked: " + cerr.Message
}

// apiKeyRow checks the API accepts the key: through the organizations
// listing, or the organization check when the listing cannot list.
func (d *doctorRun) apiKeyRow(ctx context.Context) checkResult {
	app := d.app
	const name = "api-key"
	res := d.res
	if res.APIKey == "" {
		return d.apiKeyUnsetRow()
	}
	if !d.reachable {
		return row(name, checkSkip, d.keyShown()+"; checking it needs the API", "")
	}
	if _, ok := normalizeKey(res.APIKey); !ok {
		return row(
			name,
			checkFail,
			d.keyShown()+" contains characters an API key cannot have",
			"check for quotes or stray characters in it",
		)
	}
	key, _ := normalizeKey(res.APIKey)
	client, cerr := api.New(
		&config.Resolved{
			APIKey:              key,
			Organization:        res.Organization,
			APIKeySource:        "",
			OrganizationSource:  "",
			Project:             "",
			ProjectSource:       "",
			GatewayAPIKey:       "",
			GatewayAPIKeySource: "",
			SkippedProject:      "",
			SkippedProjectOrg:   "",
		},
		app.apiOptions(),
	)
	if cerr != nil {
		return row(name, checkFail, cerr.Message, "")
	}
	return d.apiKeyCheckedRow(ctx, client, key)
}

// apiKeyUnsetRow is the api-key row when no key is set: a warning when the AI Gateway key is, a failure otherwise.
func (d *doctorRun) apiKeyUnsetRow() checkResult {
	const name = "api-key"
	if d.res.GatewayAPIKey != "" {
		return row(
			name,
			checkWarn,
			"not set; only the AI Gateway is set up",
			"to use SaladCloud beyond the AI Gateway, run saladcloud login",
		)
	}
	return row(
		name,
		checkFail,
		"not set",
		"run saladcloud login, or set SALAD_API_KEY; for the AI Gateway only, run saladcloud ai-gateway login",
	)
}

// apiKeyCheckedRow is the api-key row from the API's answer: the organizations listing, or the organization check
// when the listing cannot list and there is an organization to check.
func (d *doctorRun) apiKeyCheckedRow(ctx context.Context, client *api.Client, key string) checkResult {
	const name = "api-key"
	res := d.res
	list, cerr := client.OrganizationListing(ctx)
	if cerr == nil && list == nil && res.Organization != "" && validName(res.Organization) {
		cerr = d.checkKeyThroughOrg(ctx, client)
		if cerr == nil {
			d.keyOK = true
			return row(name, checkPass, d.keyShown(), "")
		}
	}
	switch {
	case rejected(cerr):
		return row(name, checkFail, d.keyShown()+" rejected", d.keyFix(key))
	case cerr != nil:
		return row(name, checkWarn, d.keyShown()+"; "+unchecked(cerr), "try again in a minute")
	case list == nil:
		return row(
			name,
			checkWarn,
			d.keyShown()+"; cannot be checked without an organization",
			"to set one, run saladcloud use <org>",
		)
	}
	d.keyOK, d.orgs = true, list.OrgNames()
	return row(name, checkPass, d.keyShown(), "")
}

// checkKeyThroughOrg checks the key with the organization check, which the organization row then reuses unless the
// key was rejected. An organization that is not found still means the key works: nil, and the organization row
// reports the miss.
func (d *doctorRun) checkKeyThroughOrg(ctx context.Context, client *api.Client) *clierr.Error {
	cerr := client.CheckOrg(ctx)
	d.orgCheckMade, d.orgChecked = !rejected(cerr), cerr
	if cerr != nil && cerr.Code == clierr.CodeNotFound {
		return nil
	}
	return cerr
}

// keyFix is the fix for a rejected key, where it came from.
func (d *doctorRun) keyFix(key string) string {
	switch {
	case quotedKey(key):
		return "remove the quotes around it"
	case d.res.APIKeySource != settingFromEnv:
		return "run saladcloud login to save a new key"
	case d.saved.APIKey != "" && d.saved.APIKey != d.res.APIKey:
		return fmt.Sprintf(
			"remove SALAD_API_KEY to use the saved API key (%s), or set it to a working key",
			maskAPIKey(d.saved.APIKey),
		)
	}
	return "set SALAD_API_KEY to a working key, or remove it and run saladcloud login"
}

// checkOrg is the organization check, made once.
func (d *doctorRun) checkOrg(ctx context.Context) *clierr.Error {
	app := d.app
	if !d.orgCheckMade {
		key, _ := normalizeKey(d.res.APIKey)
		client, cerr := api.New(
			&config.Resolved{
				APIKey:              key,
				Organization:        d.res.Organization,
				APIKeySource:        "",
				OrganizationSource:  "",
				Project:             "",
				ProjectSource:       "",
				GatewayAPIKey:       "",
				GatewayAPIKeySource: "",
				SkippedProject:      "",
				SkippedProjectOrg:   "",
			},
			app.apiOptions(),
		)
		if cerr == nil {
			cerr = client.CheckOrg(ctx)
		}
		d.orgCheckMade, d.orgChecked = true, cerr
	}
	return d.orgChecked
}

func (d *doctorRun) organizationRow(ctx context.Context) checkResult {
	app := d.app
	const name = "organization"
	org := d.res.Organization
	from := originOf(d.res.OrganizationSource, "--org", envOrganization)
	switch {
	case org != "" && app.looksLikeKey(org):
		return row(
			name,
			checkFail,
			keyLikeDetail(nameKindOrganization, from),
			keyLikeName(nameKindOrganization, from).HintText(),
		)
	case org != "" && !validName(org):
		return row(
			name,
			checkFail,
			//nolint:gocritic // shows the name as written; %q would escape it
			fmt.Sprintf(`"%s"%s is not a valid organization name`, org, fromTag(from)),
			nameGuess(nameKindOrganization, org, "use"),
		)
	}
	if !d.keyOK {
		return organizationSkippedRow(org, from)
	}
	if org == "" {
		return row(
			name,
			checkFail,
			"none set",
			hintClauses(app.orgsClause(d.orgs), "to set one, run saladcloud use <org>"),
		)
	}
	cerr := d.checkOrg(ctx)
	switch {
	case cerr == nil:
		d.orgGood = true
		return row(name, checkPass, org+fromTag(from), "")
	case cerr.Code == clierr.CodeNotFound:
		return row(
			name,
			checkFail,
			//nolint:gocritic // shows the name as written; %q would escape it
			fmt.Sprintf(`"%s"%s not found`, org, fromTag(from)),
			hintClauses(app.orgsClause(d.orgs), organizationNotFoundFix(from)),
		)
	}
	return row(name, checkWarn, org+fromTag(from)+"; "+unchecked(cerr), "try again in a minute")
}

// organizationSkippedRow is the organization row when there is no working key to check it with.
func organizationSkippedRow(org, from string) checkResult {
	if org == "" {
		return row("organization", checkSkip, "none set", "")
	}
	return row("organization", checkSkip, org+fromTag(from)+"; checking it needs an API key", "")
}

// organizationNotFoundFix is the fix for an organization that was not found, by where it came from; "" for a flag,
// whose fix is plain.
func organizationNotFoundFix(from string) string {
	switch from {
	case envOrganization:
		return "fix or remove SALAD_ORGANIZATION"
	case settingFromConfig:
		return "to switch, run saladcloud use <org>"
	}
	return ""
}

// doctorProject is the project the project row checks: its organization, where it came from, and whether it is
// the "default" fallback rather than a project that was set.
type doctorProject struct {
	org, project, from string
	fallback           bool
}

func (d *doctorRun) projectRow(ctx context.Context) checkResult {
	app := d.app
	const name = "project"
	res := d.res
	p := doctorProject{
		org:      res.Organization,
		project:  res.Project,
		from:     originOf(res.ProjectSource, projectFlagSpelling, envProject),
		fallback: res.ProjectSource == config.SourceDefault,
	}
	project, from := p.project, p.from
	if !p.fallback && app.looksLikeKey(project) {
		return row(name, checkFail, keyLikeDetail(nameKindProject, from), keyLikeName(nameKindProject, from).HintText())
	}
	if !d.orgGood {
		if p.fallback {
			return row(name, checkSkip, "none set", "")
		}
		return row(name, checkSkip, project+fromTag(from)+"; checking it needs a working organization", "")
	}
	if !p.fallback && !validName(project) {
		return row(
			name,
			checkFail,
			//nolint:gocritic // shows the name as written; %q would escape it
			fmt.Sprintf(`"%s"%s is not a valid project name`, project, fromTag(from)),
			nameGuess(nameKindProject, project, "use"),
		)
	}
	return d.projectCheckedRow(ctx, &p)
}

// projectCheckedRow is the project row from the project check.
func (d *doctorRun) projectCheckedRow(ctx context.Context, p *doctorProject) checkResult {
	const name = "project"
	org, project, from := p.org, p.project, p.from
	cerr := d.checkProject(ctx, org, project)
	found := cerr == nil
	if cerr != nil && cerr.Code != clierr.CodeNotFound {
		shown := project + fromTag(from)
		if p.fallback {
			shown = `none set, using "default"`
		}
		return row(name, checkWarn, shown+"; "+unchecked(cerr), "try again in a minute")
	}
	switch {
	case p.fallback:
		return d.projectFallbackRow(org, found)
	case found:
		return row(name, checkPass, project+fromTag(from), "")
	}
	fix := fmt.Sprintf("to switch, run saladcloud use %s/<project>", org)
	switch from {
	case envProject:
		fix = "fix or remove SALAD_PROJECT, or create it at " + projectCreateURL(org)
	case projectFlagSpelling:
		fix = "fix --project, or create it at " + projectCreateURL(org)
	}
	//nolint:gocritic // shows the name as written; %q would escape it
	detail := fmt.Sprintf(`"%s"%s not found in %s`, project, fromTag(from), org)
	return row(name, checkFail, detail, fix)
}

// checkProject is the project check for project in org.
func (d *doctorRun) checkProject(ctx context.Context, org, project string) *clierr.Error {
	key, _ := normalizeKey(d.res.APIKey)
	client, cerr := api.New(
		&config.Resolved{
			APIKey:              key,
			Organization:        org,
			Project:             project,
			APIKeySource:        "",
			OrganizationSource:  "",
			ProjectSource:       "",
			GatewayAPIKey:       "",
			GatewayAPIKeySource: "",
			SkippedProject:      "",
			SkippedProjectOrg:   "",
		},
		d.app.apiOptions(),
	)
	if cerr == nil {
		cerr = client.CheckProject(ctx)
	}
	return cerr
}

// projectFallbackRow is the project row when none is set and "default" is used: a project saved for another
// organization, a "default" that was found, or none.
func (d *doctorRun) projectFallbackRow(org string, found bool) checkResult {
	const name = "project"
	app := d.app
	res := d.res
	switch {
	case res.SkippedProject != "":
		return row(
			name,
			checkWarn,
			fmt.Sprintf( //nolint:gocritic // shows the saved names as written; %q would escape them
				`none set for %s, using "default"; the saved project "%s" is for %s`,
				org,
				app.shownName(res.SkippedProject),
				app.shownName(res.SkippedProjectOrg),
			),
			fmt.Sprintf("to choose one for %s, run saladcloud use %s/<project>", org, org),
		)
	case found:
		return row(
			name,
			checkWarn,
			`none set, using "default"`,
			fmt.Sprintf(
				"container group and queue commands warn each time; "+
					"to choose one, set SALAD_PROJECT or run saladcloud use %s/<project>",
				org,
			),
		)
	}
	return row(
		name,
		checkWarn,
		fmt.Sprintf(`none set; %s has no "default" project`, org),
		fmt.Sprintf(
			"container groups and queues need one; create one at %s, then run saladcloud use %s/<project>",
			projectCreateURL(org),
			org,
		),
	)
}

func (d *doctorRun) gatewayRow() checkResult {
	const name = "ai-gateway-api-key-present"
	if d.res.GatewayAPIKey == "" {
		return row(name, checkSkip, "none (optional)", "to use the AI Gateway, run saladcloud ai-gateway login")
	}
	from := originOf(d.res.GatewayAPIKeySource, "", envGatewayKey)
	return row(name, checkPass, fmt.Sprintf("%s (from %s; not tested)", maskAPIKey(d.res.GatewayAPIKey), from), "")
}

func (d *doctorRun) sshRow(ctx context.Context) checkResult {
	app := d.app
	const name = "local-ssh-key-present"
	home, err := config.HomeDir(app.Env)
	if err != nil {
		return row(name, checkWarn, "cannot find the home folder: "+config.Reason(err), "")
	}
	pub, ok := findLocalPublicKey(home)
	if !ok {
		return row(
			name,
			checkSkip,
			"none (optional)",
			"to use container-groups ssh, cp or sftp, run saladcloud ssh-key setup",
		)
	}
	if _, err := app.LookPath("ssh"); err != nil {
		return row(name, checkWarn, "key found, but ssh is not installed", app.sshMissingHint("ssh"))
	}
	priv := strings.TrimSuffix(pub, ".pub")
	if check := app.HasPassphrase(ctx, priv); check.known && check.has {
		hint := fmt.Sprintf(
			"run ssh-add %s first; to generate a key without one, run saladcloud ssh-key setup --generate",
			priv,
		)
		if isDedicatedKey(priv) {
			hint = fmt.Sprintf("run ssh-add %s first", priv)
		}
		return row(name, checkWarn, pub+" has a passphrase, which cp and one-shot ssh commands cannot ask for", hint)
	}
	return row(name, checkPass, pub+" (registration not checked)", "")
}

// keyLikeDetail is a doctor row's detail for a name that looks like a key:
// where it came from, never the value.
func keyLikeDetail(kind, from string) string {
	return strings.TrimSpace(fromTag(from)) + " looks like an API key, not " + article(kind) + " " + kind + " name"
}
