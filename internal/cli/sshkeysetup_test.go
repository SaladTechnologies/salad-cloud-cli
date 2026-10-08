package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// stubKeygen makes generateKey write a key pair instead of running
// ssh-keygen, and ssh-keygen present or not.
func stubKeygen(t *testing.T, installed bool) {
	t.Helper()
	oldGen, oldLook := generateKey, lookPath
	generateKey = func(keyPath string) error {
		if err := os.WriteFile(keyPath, []byte("private"), 0o600); err != nil {
			return err
		}
		return os.WriteFile(keyPath+".pub", []byte("ssh-ed25519 AAAAgenerated saladcloud-cli\n"), 0o600)
	}
	lookPath = func(name string) (string, error) {
		if installed {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { generateKey, lookPath = oldGen, oldLook })
}

func stubPassphrase(t *testing.T, has bool) {
	t.Helper()
	old := hasPassphrase
	hasPassphrase = func(string) (bool, bool) { return has, true }
	t.Cleanup(func() { hasPassphrase = old })
}

func TestSSHKeySetupBranches(t *testing.T) {
	stubKeygen(t, true)
	own := filepath.Join("<home>", ".ssh", "saladcloud_ed25519")
	found := filepath.Join("<home>", ".ssh", "id_ed25519")
	setup := []string{"ssh-key", "setup"}
	const register = "Register the public key at https://portal.salad.com/ssh-keys\nYour account holds one SSH key: registering this one replaces any other.\n"
	const connect = "hint: to connect to a running instance, run saladcloud container-groups ssh <group> [--instance <id>]\n"
	generated := "Generated " + own + "\n\nPublic key (" + own + ".pub):\n  ssh-ed25519 AAAAgenerated saladcloud-cli\n\n" + register
	shown := "Public key (" + found + ".pub):\n  ssh-ed25519 AAAA test@host\n\n" + register
	ask := "No SSH key found. Generate one at " + own + "? [Y/n]: "
	cancelled := "saladcloud: error: ssh-key setup cancelled, no key generated\n"
	docGen := map[string]any{"path": own + ".pub", "public_key": "ssh-ed25519 AAAAgenerated saladcloud-cli", "generated": true, "has_passphrase": false, "action": "generated", "ok": true}
	t.Run("no passphrase", func(t *testing.T) {
		stubPassphrase(t, false)
		runBranches(t, []branchCase{
			{id: "no key, Enter", s: session{args: setup, tty: []string{""}}, device: ask + "\n", stdout: generated, stderr: connect, doc: docGen},
			{id: "no key, y", s: session{args: setup, tty: []string{"y"}}, device: ask + "y\n", stdout: generated, stderr: connect, doc: docGen},
			{id: "no key, n", s: session{args: setup, tty: []string{"n"}}, code: 1, kind: "interrupted", device: ask + "n\n", stderr: cancelled},
			{id: "no key, Ctrl-C", s: session{args: setup, tty: []string{keyCtrlC}}, code: 1, kind: "interrupted", device: ask + "\n", stderr: cancelled},
			{id: "no key, end of input", s: session{args: setup, tty: []string{keyEOF}}, code: 1, kind: "interrupted", device: ask + "\n", stderr: cancelled},
			{id: "no key, no terminal", s: session{args: setup}, code: 2, kind: "usage",
				stderr: "saladcloud: error: no local SSH key found, and no terminal to ask whether to generate one\nhint: to generate one, run saladcloud ssh-key setup --generate\n"},
			{id: "no key, --generate", s: session{args: []string{"ssh-key", "setup", "--generate"}}, stdout: generated, stderr: connect, doc: docGen},
			{id: "a key found", s: session{args: setup, sshKey: true}, stdout: shown, stderr: connect,
				doc: map[string]any{"path": found + ".pub", "generated": false, "has_passphrase": false, "action": "found"}},
			{id: "the CLI's own key file without its public half", s: session{args: []string{"ssh-key", "setup", "--generate"}, sshKey: true, sshKeyName: "saladcloud_ed25519"},
				code: 1, kind: "api_error",
				stderr: "saladcloud: error: cannot generate a key: " + own + " already exists\nhint: register " + own + ".pub, or remove it to generate a new one\n"},
		})
	})
	t.Run("a passphrase", func(t *testing.T) {
		stubPassphrase(t, true)
		warn := "warning: " + found + " has a passphrase, which cp and one-shot ssh commands cannot ask for\nhint: to use it with those, run ssh-add " + found + " first\n"
		offer := "Generate a key without a passphrase at " + own + "? The CLI will use it instead, and registering it replaces your account's current key. [y/N]: "
		runBranches(t, []branchCase{
			{id: "Enter keeps the found key", s: session{args: setup, sshKey: true, tty: []string{""}}, device: offer + "\n",
				stdout: shown, stderr: warn + connect, doc: map[string]any{"action": "found", "has_passphrase": true}},
			{id: "y generates", s: session{args: setup, sshKey: true, tty: []string{"y"}}, device: offer + "y\n",
				stdout: generated, stderr: warn + connect, doc: docGen},
			{id: "Ctrl-C", s: session{args: setup, sshKey: true, tty: []string{keyCtrlC}}, code: 1, kind: "interrupted", device: offer + "\n",
				stderr: warn + cancelled},
			{id: "no terminal", s: session{args: setup, sshKey: true}, stdout: shown, stderr: warn + connect, doc: map[string]any{"action": "found", "has_passphrase": true}},
			{id: "the CLI's own key", s: session{args: setup, sshKey: true, sshKeyName: "saladcloud_ed25519.pub", tty: []string{}},
				stdout: "Public key (" + own + ".pub):\n  ssh-ed25519 AAAA test@host\n\n" + register,
				stderr: "warning: " + own + " has a passphrase, which cp and one-shot ssh commands cannot ask for\nhint: to use it with those, run ssh-add " + own + " first\n" + connect},
		})
	})
	t.Run("ssh-keygen missing", func(t *testing.T) {
		stubKeygen(t, false)
		stubPassphrase(t, false)
		for _, tc := range []struct{ os, hint string }{
			{"windows", "add the OpenSSH Client optional feature in Windows Settings, then open a new terminal"},
			{"darwin", "macOS includes it at /usr/bin/ssh-keygen; check that /usr/bin is on your PATH"},
			{"linux", "install your distribution's OpenSSH client (openssh-client on Debian and Ubuntu, openssh-clients on Fedora)"},
		} {
			runBranches(t, []branchCase{{id: tc.os, s: session{args: []string{"ssh-key", "setup", "--generate"}, os: tc.os}, code: 1, kind: "api_error",
				stderr: "saladcloud: error: ssh-keygen not found\nhint: " + tc.hint + "\n"}})
		}
	})
}
