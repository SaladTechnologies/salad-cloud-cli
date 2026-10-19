package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// apiHost is how doctor names the production API; productionAPI is where
// it probes it (tests point it at a stand-in).
const apiHost = "https://api.salad.com"

var productionAPI = api.DefaultBaseURL

// lookPath and hasPassphrase are the system checks the SSH row makes;
// tests replace them.
var (
	lookPath      = exec.LookPath
	hasPassphrase = keyHasPassphrase
)

func init() {
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
		RunE: runDoctor,
	}
	Register(cmd)
}

// doctorRun is one run's findings, which later checks read.
type doctorRun struct {
	ctx                   context.Context
	orgFlag, projectFlag  string
	saved                 config.File
	res                   config.Resolved
	reachable             bool
	keyOK                 bool
	orgs                  []string      // the key's organizations; nil: unknown
	orgChecked            *clierr.Error // the organization check, once made
	orgCheckMade, orgGood bool
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	fr := readFlags(cmd.Flags())
	orgFlag := fr.String("org")
	projectFlag := fr.String("project")
	if cerr := fr.Err(); cerr != nil {
		return cerr
	}
	d := &doctorRun{ctx: cmd.Context(), orgFlag: orgFlag, projectFlag: projectFlag}
	rows := []checkResult{
		d.configRow(), d.reachableRow(), d.apiKeyRow(), d.organizationRow(), d.projectRow(), d.gatewayRow(), d.sshRow(),
	}

	var failed []string
	for _, r := range rows {
		if r.Status == checkFail {
			failed = append(failed, r.Name)
		}
	}
	// The variables that hide a different saved value: the warning after
	// the table, and env_overrides beside the rows.
	var ovs []override
	for _, o := range overridesOf(d.saved, envAPIKey, envOrganization, envProject, envGatewayKey) {
		if o.saved != "" {
			ovs = append(ovs, o)
		}
	}
	err := output.Emit(doctorResult{Items: rows, EnvOverrides: overrideNames(ovs)}, func(w *output.Writer) {
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
			hintLine("%s: %s", r.Name, *r.Hint)
		}
	}
	warnOverrides(errw(), asSaved, ovs)
	if len(failed) == 0 {
		return nil
	}
	checks := "checks"
	if len(failed) == 1 {
		checks = "check"
	}
	hint := "each failing row's hint above says how to fix it"
	if output.Machine() {
		hint = "each failing row's hint says how to fix it"
	}
	return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "%d %s failed: %s", len(failed), checks, strings.Join(failed, ", ")).
		WithHint("%s", hint)
}

func row(name, status, detail, hint string) checkResult {
	r := checkResult{Name: name, Status: status, Detail: detail}
	if hint != "" {
		r.Hint = &hint
	}
	return r
}

// configRow checks the config file; the run's settings resolve from it, or
// from flags and the environment alone when it cannot be read.
func (d *doctorRun) configRow() checkResult {
	path, err := config.Path()
	if err != nil {
		d.res = config.ResolveFrom(config.File{}, d.orgFlag, d.projectFlag)
		return row("config", checkFail, "no config location: "+config.Reason(err),
			"set SALAD_CLI_CONFIG_DIR, or use SALAD_API_KEY, SALAD_ORGANIZATION and SALAD_PROJECT")
	}
	f, err := config.Load()
	if err != nil {
		d.res = config.ResolveFrom(config.File{}, d.orgFlag, d.projectFlag)
		return row(
			"config",
			checkFail,
			fmt.Sprintf("%s cannot be read: %s", path, readReason(err)),
			"fix or delete it, then run saladcloud login again",
		)
	}
	d.saved = f
	d.res = config.ResolveFrom(f, d.orgFlag, d.projectFlag)
	if err := config.Writable(); err != nil {
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
func (d *doctorRun) reachableRow() checkResult {
	base, shown, override := productionAPI, apiHost, os.Getenv("SALAD_CLI_API_BASE_URL")
	if override != "" {
		base, shown = override, override
	}
	ctx, cancel := context.WithTimeout(d.ctx, 10*time.Second)
	defer cancel()
	if _, err := api.Probe(ctx, base+"/organizations/doctor-probe/gpu-classes"); err != nil {
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
func (d *doctorRun) apiKeyRow() checkResult {
	const name = "api-key"
	res := d.res
	if res.APIKey == "" {
		if res.GatewayAPIKey != "" {
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
	client, cerr := api.New(config.Resolved{APIKey: key, Organization: res.Organization})
	if cerr != nil {
		return row(name, checkFail, cerr.Message, "")
	}
	list, cerr := client.OrganizationListing(d.ctx)
	if cerr == nil && list == nil && res.Organization != "" && validName(res.Organization) {
		cerr = client.CheckOrg(d.ctx)
		d.orgCheckMade, d.orgChecked = !rejected(cerr), cerr
		if cerr != nil && cerr.Code == clierr.CodeNotFound {
			cerr = nil // the key works; the organization row reports the miss
		}
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

// keyFix is the fix for a rejected key, where it came from.
func (d *doctorRun) keyFix(key string) string {
	switch {
	case quotedKey(key):
		return "remove the quotes around it"
	case d.res.APIKeySource != "env":
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
func (d *doctorRun) checkOrg() *clierr.Error {
	if !d.orgCheckMade {
		key, _ := normalizeKey(d.res.APIKey)
		client, cerr := api.New(config.Resolved{APIKey: key, Organization: d.res.Organization})
		if cerr == nil {
			cerr = client.CheckOrg(d.ctx)
		}
		d.orgCheckMade, d.orgChecked = true, cerr
	}
	return d.orgChecked
}

func (d *doctorRun) organizationRow() checkResult {
	const name = "organization"
	org := d.res.Organization
	from := originOf(d.res.OrganizationSource, "--org", envOrganization)
	switch {
	case org != "" && looksLikeKey(org):
		return row(name, checkFail, keyLikeDetail("organization", from), keyLikeName("organization", from).HintText())
	case org != "" && !validName(org):
		return row(
			name,
			checkFail,
			fmt.Sprintf(`"%s"%s is not a valid organization name`, org, fromTag(from)),
			nameGuess("organization", org, "use"),
		)
	}
	if !d.keyOK {
		if org == "" {
			return row(name, checkSkip, "none set", "")
		}
		return row(name, checkSkip, org+fromTag(from)+"; checking it needs an API key", "")
	}
	if org == "" {
		return row(name, checkFail, "none set", hintClauses(orgsClause(d.orgs), "to set one, run saladcloud use <org>"))
	}
	cerr := d.checkOrg()
	switch {
	case cerr == nil:
		d.orgGood = true
		return row(name, checkPass, org+fromTag(from), "")
	case cerr.Code == clierr.CodeNotFound:
		fix := ""
		switch from {
		case envOrganization:
			fix = "fix or remove SALAD_ORGANIZATION"
		case "config":
			fix = "to switch, run saladcloud use <org>"
		}
		return row(
			name,
			checkFail,
			fmt.Sprintf(`"%s"%s not found`, org, fromTag(from)),
			hintClauses(orgsClause(d.orgs), fix),
		)
	}
	return row(name, checkWarn, org+fromTag(from)+"; "+unchecked(cerr), "try again in a minute")
}

func (d *doctorRun) projectRow() checkResult {
	const name = "project"
	res := d.res
	org, project := res.Organization, res.Project
	from := originOf(res.ProjectSource, "--project", envProject)
	fallback := res.ProjectSource == config.SourceDefault
	if !fallback && looksLikeKey(project) {
		return row(name, checkFail, keyLikeDetail("project", from), keyLikeName("project", from).HintText())
	}
	if !d.orgGood {
		if fallback {
			return row(name, checkSkip, "none set", "")
		}
		return row(name, checkSkip, project+fromTag(from)+"; checking it needs a working organization", "")
	}
	if !fallback && !validName(project) {
		return row(
			name,
			checkFail,
			fmt.Sprintf(`"%s"%s is not a valid project name`, project, fromTag(from)),
			nameGuess("project", project, "use"),
		)
	}
	key, _ := normalizeKey(res.APIKey)
	client, cerr := api.New(config.Resolved{APIKey: key, Organization: org, Project: project})
	if cerr == nil {
		cerr = client.CheckProject(d.ctx)
	}
	found := cerr == nil
	if cerr != nil && cerr.Code != clierr.CodeNotFound {
		shown := project + fromTag(from)
		if fallback {
			shown = `none set, using "default"`
		}
		return row(name, checkWarn, shown+"; "+unchecked(cerr), "try again in a minute")
	}
	switch {
	case fallback && res.SkippedProject != "":
		return row(
			name,
			checkWarn,
			fmt.Sprintf(
				`none set for %s, using "default"; the saved project "%s" is for %s`,
				org,
				shownName(res.SkippedProject),
				shownName(res.SkippedProjectOrg),
			),
			fmt.Sprintf("to choose one for %s, run saladcloud use %s/<project>", org, org),
		)
	case fallback && found:
		return row(
			name,
			checkWarn,
			`none set, using "default"`,
			fmt.Sprintf(
				"container group and queue commands warn each time; to choose one, set SALAD_PROJECT or run saladcloud use %s/<project>",
				org,
			),
		)
	case fallback:
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
	case found:
		return row(name, checkPass, project+fromTag(from), "")
	}
	fix := fmt.Sprintf("to switch, run saladcloud use %s/<project>", org)
	switch from {
	case envProject:
		fix = "fix or remove SALAD_PROJECT, or create it at " + projectCreateURL(org)
	case "--project":
		fix = "fix --project, or create it at " + projectCreateURL(org)
	}
	return row(name, checkFail, fmt.Sprintf(`"%s"%s not found in %s`, project, fromTag(from), org), fix)
}

func (d *doctorRun) gatewayRow() checkResult {
	const name = "ai-gateway-api-key-present"
	if d.res.GatewayAPIKey == "" {
		return row(name, checkSkip, "none (optional)", "to use the AI Gateway, run saladcloud ai-gateway login")
	}
	from := originOf(d.res.GatewayAPIKeySource, "", envGatewayKey)
	return row(name, checkPass, fmt.Sprintf("%s (from %s; not tested)", maskAPIKey(d.res.GatewayAPIKey), from), "")
}

func (*doctorRun) sshRow() checkResult {
	const name = "local-ssh-key-present"
	home, err := os.UserHomeDir()
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
	if _, err := lookPath("ssh"); err != nil {
		return row(name, checkWarn, "key found, but ssh is not installed", sshMissingHint("ssh"))
	}
	priv := strings.TrimSuffix(pub, ".pub")
	if enc, known := hasPassphrase(priv); known && enc {
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
