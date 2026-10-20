package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeKey creates home/.ssh/name with placeholder contents.
func writeKey(t *testing.T, home, name string) string {
	t.Helper()
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("ssh-ed25519 AAAA test@host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFindLocalPublicKeyNone(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if path, ok := findLocalPublicKey(home); ok {
		t.Errorf("found %q in empty home, want none", path)
	}
}

func TestFindLocalPublicKeyOrder(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	rsa := writeKey(t, home, "id_rsa.pub")
	salad := writeKey(t, home, "saladcloud_ed25519.pub")
	_ = salad

	// The CLI's own generated key wins: it is the identity ssh/cp/sftp
	// offer by default, so it is the one to show and register.
	if path, ok := findLocalPublicKey(home); !ok || path != salad {
		t.Errorf("found %q, want %q", path, salad)
	}
	_ = rsa

	// Without it, id_ed25519.pub beats id_rsa.pub.
	other := t.TempDir()
	rsa2 := writeKey(t, other, "id_rsa.pub")
	if path, ok := findLocalPublicKey(other); !ok || path != rsa2 {
		t.Errorf("found %q, want %q", path, rsa2)
	}
	ed := writeKey(t, other, "id_ed25519.pub")
	if path, ok := findLocalPublicKey(other); !ok || path != ed {
		t.Errorf("found %q, want %q", path, ed)
	}
}

func TestFindLocalPublicKeyIgnoresDirectories(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh", "id_ed25519.pub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if path, ok := findLocalPublicKey(home); ok {
		t.Errorf("found directory %q as a key, want none", path)
	}
}

// TestResolveIdentity: the flag wins; else the dedicated key when present;
// else "" so OpenSSH uses its own defaults.
func TestResolveIdentity(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	home := t.TempDir()
	app.setenv("HOME", home)
	app.setenv("USERPROFILE", home)
	if got := app.resolveIdentity("/explicit/key"); got != "/explicit/key" {
		t.Errorf("flag = %q", got)
	}
	if got := app.resolveIdentity(""); got != "" {
		t.Errorf("no dedicated key: %q, want empty", got)
	}
	priv := filepath.Join(home, ".ssh", sshDedicatedKeyName)
	if err := os.MkdirAll(filepath.Dir(priv), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(priv, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := app.resolveIdentity(""); got != priv {
		t.Errorf("dedicated key present: %q, want %q", got, priv)
	}
	if got := app.resolveIdentity("/explicit/key"); got != "/explicit/key" {
		t.Errorf("flag must still win: %q", got)
	}
}

// TestKeyHasPassphrase generates real keys with and without a passphrase
// and checks the detection; skipped where ssh-keygen is unavailable.
func TestKeyHasPassphrase(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not on PATH")
	}
	dir := t.TempDir()
	keygen := func(path, passphrase string) {
		t.Helper()
		//nolint:gosec // generates a test key under t.TempDir()
		cmd := exec.CommandContext(t.Context(), "ssh-keygen", "-q", "-t", "ed25519", "-N", passphrase, "-f", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v %s", err, out)
		}
	}
	plain := filepath.Join(dir, "plain")
	keygen(plain, "")
	locked := filepath.Join(dir, "locked")
	keygen(locked, "correct horse")
	if c := keyHasPassphrase(t.Context(), plain); !c.known || c.has {
		t.Errorf("plain key: encrypted=%v known=%v, want false/true", c.has, c.known)
	}
	if c := keyHasPassphrase(t.Context(), locked); !c.known || !c.has {
		t.Errorf("locked key: encrypted=%v known=%v, want true/true", c.has, c.known)
	}
	if c := keyHasPassphrase(t.Context(), filepath.Join(dir, "missing")); c.known {
		t.Error("missing key must be unknown, not a verdict")
	}
}
