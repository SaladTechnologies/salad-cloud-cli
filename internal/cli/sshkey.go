// ssh-key manages the LOCAL half of SSH auth for 'saladcloud container-groups ssh'/'cp'.
// The platform model is one registered public key per user account
// (ssh-ed25519 or ssh-rsa), registered in the portal, so setup
// discovers/generates a local key pair and hands the public key to the
// user for portal registration.

package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// sshDedicatedKeyName is the key pair 'ssh-key setup' generates (no
// passphrase, so one-shot commands work), under ~/.ssh.
const sshDedicatedKeyName = "saladcloud_ed25519"

// sshDirMode is the mode ssh-key setup creates ~/.ssh with: OpenSSH refuses a key folder others can read.
const sshDirMode = 0o700

// The actions ssh-key setup reports: a local key found, or the CLI's own generated.
const (
	sshKeyActionFound     = "found"
	sshKeyActionGenerated = "generated"
)

// setupVerbGet is the get verb's name under ssh-key.
const setupVerbGet = "get"

// sshPublicKeyCandidates are the public key files probed, in order, under
// ~/.ssh. The CLI's own generated key comes first: when it exists it is the
// one 'ssh'/'cp'/'sftp' offer by default (see resolveIdentity), so it is
// the one to show and register.
func sshPublicKeyCandidates() []string {
	return []string{sshDedicatedKeyName + ".pub", "id_ed25519.pub", "id_rsa.pub"}
}

// defaultIdentityPath returns the private half of the CLI's dedicated key
// when it exists under home/.ssh.
func defaultIdentityPath(home string) (string, bool) {
	path := filepath.Join(home, ".ssh", sshDedicatedKeyName)
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path, true
	}
	return "", false
}

// resolveIdentity decides which private key ssh/cp/sftp pass with -i: the
// --identity flag when given; else the CLI's dedicated key when it exists;
// else nothing, leaving OpenSSH to its default identities. Without this the
// key that 'ssh-key setup' generates — a non-default filename OpenSSH never
// tries on its own — did not work with the CLI's own verbs until the user
// ran ssh-add or wrote an ssh_config entry (found on the first macOS pass).
func (app *App) resolveIdentity(flag string) string {
	if flag != "" {
		return flag
	}
	home, err := config.HomeDir(app.Env)
	if err != nil {
		return ""
	}
	if p, ok := defaultIdentityPath(home); ok {
		return p
	}
	return ""
}

// keyHasPassphrase reports whether the private key at path is encrypted,
// by asking ssh-keygen to derive the public key with an empty passphrase.
// known is false when that could not be determined (no ssh-keygen on PATH,
// unreadable file). A passphrase-protected key matters because one-shot
// 'ssh -- <cmd>' runs in BatchMode, which cannot prompt for it, so the key
// fails exactly like an unregistered one.
func keyHasPassphrase(ctx context.Context, path string) passphraseCheck {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		return passphraseCheck{has: false, known: false}
	}
	if _, err := os.Stat(path); err != nil {
		return passphraseCheck{has: false, known: false}
	}
	cmd := exec.CommandContext(ctx, "ssh-keygen", "-y", "-P", "", "-f", path) //nolint:gosec // the key being checked
	if err := cmd.Run(); err != nil {
		return passphraseCheck{has: true, known: true}
	}
	return passphraseCheck{has: false, known: true}
}

// passphraseCheck is whether a private key has a passphrase, and whether that could be told at all.
type passphraseCheck struct {
	has, known bool
}

// warnPassphrase says a key has a passphrase that one-shot commands cannot
// ask for, and how to load it first.
func (app *App) warnPassphrase(priv string) {
	fprintf(app.errw(), "warning: %s has a passphrase, which cp and one-shot ssh commands cannot ask for\n", priv)
	app.hintLine("to use it with those, run ssh-add %s first", priv)
}

// sshKeysPage is the portal page where the account's SSH key is registered.
const sshKeysPage = "https://portal.salad.com/ssh-keys"

// sshKeygen runs ssh-keygen for a key without a passphrase at keyPath.
func sshKeygen(ctx context.Context, keyPath string) error {
	args := []string{"-t", "ed25519", "-f", keyPath, "-N", "", "-C", "saladcloud-cli"}
	keygen := exec.CommandContext(ctx, "ssh-keygen", args...) //nolint:gosec // the CLI's own key under ~/.ssh
	out, err := keygen.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w; %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// findLocalPublicKey returns the first existing candidate under
// home/.ssh, or ("", false) when none exists.
func findLocalPublicKey(home string) (string, bool) {
	for _, name := range sshPublicKeyCandidates() {
		path := filepath.Join(home, ".ssh", name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, true
		}
	}
	return "", false
}

// sshKeyResult is the Emit payload for ssh-key setup/get.
type sshKeyResult struct {
	Path      string `json:"path"`
	PublicKey string `json:"public_key"`
	Generated bool   `json:"generated"`
	// HasPassphrase is whether the private key has a passphrase, which cp
	// and one-shot ssh commands cannot ask for; null when it cannot be told
	// (no ssh-keygen).
	HasPassphrase *bool `json:"has_passphrase"`
}

// sshKeySetupResult is ssh-key setup's JSON contract: the key, plus the
// action shape every state-changing command carries; action is
// "generated" or "found".
type sshKeySetupResult struct {
	Path          string `json:"path"`
	PublicKey     string `json:"public_key"`
	Generated     bool   `json:"generated"`
	HasPassphrase *bool  `json:"has_passphrase"`
	Action        string `json:"action"`
	OK            bool   `json:"ok"`
}

// passphraseAt is has_passphrase for the private key at priv: whether it has
// one, or null when that could not be told.
func (app *App) passphraseAt(ctx context.Context, priv string) *bool {
	check := app.HasPassphrase(ctx, priv)
	encrypted, known := check.has, check.known
	if !known {
		return nil
	}
	return &encrypted
}

// readPublicKey loads and trims one public key file.
func readPublicKey(path string) (string, *clierr.Error) {
	data, err := os.ReadFile(path) //nolint:gosec // a public key file under ~/.ssh, which the command exists to show
	if err != nil {
		return "", clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot read the public key %s: %s",
			path,
			config.Reason(err),
		)
	}
	return strings.TrimSpace(string(data)), nil
}

// sshKeySetup is one run of ssh-key setup.
type sshKeySetup struct {
	keyPath      string // the CLI's own key, without .pub
	generateFlag bool   // --generate
	app          *App   // the run
}

func (app *App) runSSHKeySetup(ctx context.Context, generate bool) error {
	home, err := config.HomeDir(app.Env)
	if err != nil {
		return clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot find the home folder: %s",
			config.Reason(err),
		)
	}
	k := sshKeySetup{keyPath: filepath.Join(home, ".ssh", sshDedicatedKeyName), generateFlag: generate, app: app}
	path, found := findLocalPublicKey(home)
	if found {
		return k.useFound(ctx, path)
	}
	return k.setupMissing(ctx)
}

// useFound shows the local key found at path, unless it has a passphrase and the CLI's own key is to replace it.
func (k sshKeySetup) useFound(ctx context.Context, path string) error {
	app := k.app
	priv := strings.TrimSuffix(path, ".pub")
	passphrase := app.passphraseAt(ctx, priv)
	if passphrase != nil && *passphrase {
		app.warnPassphrase(priv)
		replace, cerr := k.replaceWithDedicated(priv)
		if cerr != nil {
			return cerr
		}
		if replace {
			return k.generate(ctx)
		}
	}
	return k.show(path, sshKeyActionFound, passphrase)
}

// replaceWithDedicated reports whether to generate the CLI's own key in place of priv, a key with a passphrase:
// with --generate, or when the person at a terminal says yes; never when priv is already the CLI's own key.
func (k sshKeySetup) replaceWithDedicated(priv string) (bool, *clierr.Error) {
	app := k.app
	switch {
	case isDedicatedKey(priv):
		return false, nil
	case k.generateFlag:
		return true, nil
	case app.atTerminal():
		answer, err := app.ask(
			fmt.Sprintf(
				"Generate a key without a passphrase at %s? The CLI will use it instead, "+
					"and registering it replaces your account's current key. [y/N]:",
				k.keyPath,
			),
		)
		if err != nil {
			return false, setupCanceled()
		}
		return yes(answer), nil
	}
	return false, nil
}

// setupMissing generates the CLI's own key when there is no local key: with --generate, or when the person at a
// terminal does not say no; an error without a terminal.
func (k sshKeySetup) setupMissing(ctx context.Context) error {
	app := k.app
	if !k.generateFlag {
		if !app.atTerminal() {
			return clierr.New(
				clierr.CodeUsage,
				clierr.KindUsage,
				"no local SSH key found, and no terminal to ask whether to generate one",
			).WithHint("to generate one, run saladcloud ssh-key setup --generate")
		}
		answer, err := app.ask(fmt.Sprintf("No SSH key found. Generate one at %s? [Y/n]:", k.keyPath))
		if err != nil || answer != "" && !yes(answer) {
			return setupCanceled()
		}
	}
	return k.generate(ctx)
}

// yes reports whether a yes/no answer is yes.
func yes(answer string) bool {
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")
}

// setupCanceled is the error for "n", Ctrl-C or end of input at the
// generate prompt.
func setupCanceled() *clierr.Error {
	return clierr.New(clierr.CodeGeneric, clierr.KindInterrupted, "ssh-key setup canceled, no key generated")
}

// generate creates the CLI's own key and shows it.
func (k sshKeySetup) generate(ctx context.Context) error {
	app := k.app
	if _, err := os.Stat(k.keyPath); err == nil {
		return clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot generate a key: %s already exists",
			k.keyPath,
		).WithHint("register %s.pub, or remove it to generate a new one", k.keyPath)
	}
	if _, err := app.LookPath("ssh-keygen"); err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "ssh-keygen not found").
			WithHint("%s", app.sshMissingHint("ssh-keygen"))
	}
	if err := os.MkdirAll(filepath.Dir(k.keyPath), sshDirMode); err != nil {
		return clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot create %s: %s",
			filepath.Dir(k.keyPath),
			config.Reason(err),
		)
	}
	if err := app.GenerateKey(ctx, k.keyPath); err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "ssh-keygen failed: %v", err)
	}
	return k.show(k.keyPath+".pub", sshKeyActionGenerated, ptr.To(false))
}

// show prints the public key at path and where to register it; action is sshKeyActionFound or
// sshKeyActionGenerated.
func (k sshKeySetup) show(path, action string, passphrase *bool) error {
	app := k.app
	key, cerr := readPublicKey(path)
	if cerr != nil {
		return cerr
	}
	generated := action == sshKeyActionGenerated
	v := sshKeySetupResult{
		Path:          path,
		PublicKey:     key,
		Generated:     generated,
		HasPassphrase: passphrase,
		Action:        action,
		OK:            true,
	}
	err := app.printer.Emit(v, func(w *output.Writer) {
		if generated {
			w.Printf("Generated %s\n\n", strings.TrimSuffix(path, ".pub"))
		}
		w.Printf("Public key (%s):\n  %s\n\n", path, key)
		w.Printf("Register the public key at %s\n", sshKeysPage)
		w.Println("Your account holds one SSH key: registering this one replaces any other.")
	})
	if err != nil {
		return wrapErr(err)
	}
	if app.printer.Machine() {
		fprintf(
			app.errw(),
			"note: register the public key at %s; "+
				"your account holds one SSH key, so registering this one replaces any other\n",
			sshKeysPage,
		)
	}
	app.hintLine("to connect to a running instance, run saladcloud container-groups ssh <group> [--instance <id>]")
	return nil
}

func (app *App) runSSHKeyGet(cmd *cobra.Command, _ []string) error {
	home, err := config.HomeDir(app.Env)
	if err != nil {
		return clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot find the home folder: %s",
			config.Reason(err),
		)
	}
	path, found := findLocalPublicKey(home)
	if !found {
		return clierr.New(clierr.CodeNotFound, clierr.KindNotFound, "no local SSH key found").
			WithHint("to find or generate one, run saladcloud ssh-key setup")
	}
	key, cerr := readPublicKey(path)
	if cerr != nil {
		return cerr
	}
	passphrase := app.passphraseAt(cmd.Context(), strings.TrimSuffix(path, ".pub"))
	if passphrase != nil && *passphrase {
		app.warnPassphrase(strings.TrimSuffix(path, ".pub"))
	}
	v := sshKeyResult{Path: path, PublicKey: key, HasPassphrase: passphrase, Generated: false}
	return wrapErr(app.printer.Emit(v, func(w *output.Writer) {
		w.Printf("%s\n%s\n", path, key)
	}))
}

// addSSHKeyCommands adds sshkey.go's commands to the tree.
func (app *App) addSSHKeyCommands(root *cobra.Command) {
	sshKeyCmd := &cobra.Command{
		Use:   "ssh-key",
		Short: "The SSH key that container-groups ssh, cp, and sftp authenticate with (get, setup)",
		Long: `The local SSH key that saladcloud container-groups ssh, sftp and cp use.

Your account holds one SSH public key (ssh-ed25519 or ssh-rsa), registered in
the portal at ` + sshKeysPage + `; registering a key replaces any other.
The API does not show the registered key, so the CLI cannot check it.`,
	}

	var generate bool
	setupCmd := &cobra.Command{
		Use:   "setup",
		Short: "Find or generate a local SSH key, and show where to register it",
		Long: `Find a local SSH public key (~/.ssh/saladcloud_ed25519.pub, the CLI's own,
then ~/.ssh/id_ed25519.pub, then ~/.ssh/id_rsa.pub). With none, generate the
CLI's own ed25519 key without a passphrase at ~/.ssh/saladcloud_ed25519:
asked first at a terminal, without asking with --generate, and never
otherwise. Then print the public key and where to register it: ` + sshKeysPage + `.

A key with a passphrase cannot be used by cp and one-shot ssh commands until
it is loaded with ssh-add; at a terminal setup offers to generate the CLI's
own key instead.

SALAD_CLI_NO_PROMPT, or CI set to anything but empty, false or 0, makes it
behave as if there is no terminal.`,
		Example: `  # Find or generate a key, asking before generating
  saladcloud ssh-key setup

  # Scripts and CI: generate without asking when there is none
  saladcloud ssh-key setup --generate`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return app.runSSHKeySetup(cmd.Context(), generate) },
	}
	setupCmd.Flags().BoolVar(
		&generate,
		"generate",
		false,
		"generate ~/.ssh/saladcloud_ed25519 without asking when no usable key is found",
	)
	sshKeyCmd.AddCommand(setupCmd)

	sshKeyCmd.AddCommand(&cobra.Command{
		Use:   setupVerbGet,
		Short: "Print the local SSH public key that would be used",
		Long: `Print the local SSH public key (path and contents) the CLI would use, in the
same search order as saladcloud ssh-key setup: ~/.ssh/saladcloud_ed25519.pub,
then ~/.ssh/id_ed25519.pub, then ~/.ssh/id_rsa.pub. Exits 4 when there is none.

This is the key on this machine, not the one registered on your account:
the API does not expose the account's key, so whether this one is
registered cannot be checked from here.`,
		Args: cobra.NoArgs,
		RunE: app.runSSHKeyGet,
	})

	root.AddCommand(sshKeyCmd)
}
