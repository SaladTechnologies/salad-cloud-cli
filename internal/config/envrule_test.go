package config

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sharedEnv are the variables the platform, its docs and SDKs share; every
// other variable this CLI reads is SALAD_CLI_-prefixed, and no other SALAD
// spelling exists (no SALADCLOUD_, no SALAD_CONFIG_DIR).
func sharedEnv() map[string]bool {
	return map[string]bool{
		"SALAD_API_KEY":            true,
		"SALAD_ORGANIZATION":       true,
		"SALAD_PROJECT":            true,
		"SALAD_AI_GATEWAY_API_KEY": true,
	}
}

// saladVar matches any SALAD-prefixed variable name as code, scripts,
// workflows and docs spell it.
var saladVar = regexp.MustCompile(`\bSALAD(CLOUD)?_[A-Z0-9_]+\b`)

// TestEnvironmentVariableRule reads the module's Go sources, installers,
// workflows, goreleaser config, Dockerfile, justfile and docs: every SALAD
// variable is shared or SALAD_CLI_. Names the platform injects into
// containers (SALAD_ORGANIZATION_NAME, ...) are not read by the CLI and
// fail here: the rule has no tier for them.
func TestEnvironmentVariableRule(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	checked := 0
	for _, path := range ruleFiles(t, root) {
		if !ruleCovers(filepath.Base(path)) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, v := range saladVar.FindAllString(string(data), -1) {
			if !sharedEnv()[v] && !strings.HasPrefix(v, "SALAD_CLI_") {
				t.Errorf("%s: %s breaks the rule (SALAD_ for the four shared names, SALAD_CLI_ for the rest)", path, v)
			}
		}
	}
	if checked < 50 {
		t.Fatalf("checked only %d files; is the walk reaching the module?", checked)
	}
}

// ruleCovers reports whether TestEnvironmentVariableRule reads a file, by its name: the justfile, the Dockerfile,
// and Go, shell, PowerShell, YAML and Markdown files, except this one, which names the old spellings on purpose.
func ruleCovers(name string) bool {
	switch name {
	case "envrule_test.go":
		return false
	case "justfile", "Dockerfile":
		return true
	}
	switch filepath.Ext(name) {
	case ".go", ".sh", ".ps1", ".yml", ".yaml", ".md":
		return true
	}
	return false
}

// ruleFiles lists the files the rule covers: the tracked files when root is
// a git checkout, so untracked local files are not held to it, else (a copy
// without git, such as a source archive) every file outside hidden, build
// and vendor directories.
func ruleFiles(t *testing.T, root string) []string {
	t.Helper()
	if files, ok := trackedFiles(t, root); ok {
		return files
	}
	return walkedFiles(t, root)
}

// trackedFiles lists the files git tracks under root; ok is false when root is not a git checkout (or git is
// missing, or tracks nothing).
func trackedFiles(t *testing.T, root string) ([]string, bool) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "-C", root, "ls-files").Output()
	if err != nil || len(out) == 0 {
		return nil, false
	}
	var files []string
	for f := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		files = append(files, filepath.Join(root, filepath.FromSlash(f)))
	}
	return files, true
}

// walkedFiles lists every file under root outside the directories skippedRuleDir names.
func walkedFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skippedRuleDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// skippedRuleDir reports whether walkedFiles skips a directory: hidden ones but .github, and dist, vendor and
// completions.
func skippedRuleDir(name string) bool {
	return strings.HasPrefix(name, ".") && name != ".github" || name == "dist" || name == "vendor" ||
		name == "completions"
}

// envCall matches a variable name read or set in Go code.
var envCall = regexp.MustCompile(`(?:os\.Getenv|os\.LookupEnv|os\.Setenv|t\.Setenv)\("([A-Za-z0-9_]+)"`)

// systemEnv are variables the platform, the OS or a tool defines, which the
// CLI reads but does not name.
// MSYSTEM is set by Git Bash (MSYS); the CLI reads it to know a path may have
// been rewritten.
// CI is set by GitHub Actions, GitLab and most CI systems; the CLI reads it
// to behave as if there were no terminal, since some run steps under a
// pseudo-terminal where a prompt would wait forever.
// SSH_CONNECTION and SSH_TTY are set in an SSH session, and DISPLAY and
// WAYLAND_DISPLAY when Linux has a display: the CLI reads them to offer a
// browser page only where it could be seen. WSL_DISTRO_NAME is set under WSL,
// where the page opens in Windows' own browser.
func systemEnv() map[string]bool {
	return map[string]bool{
		"HOME": true, "USERPROFILE": true, "MSYSTEM": true, "CI": true,
		"SSH_CONNECTION": true, "SSH_TTY": true, "DISPLAY": true, "WAYLAND_DISPLAY": true, "WSL_DISTRO_NAME": true,
	}
}

// TestEveryVariableTheCodeNamesFollowsTheRule: a variable the Go code reads
// or sets is a SALAD_ name (held to the rule above) or a system one; a
// CLI-only knob spelled any other way (CLI_TEST_..., SALADCLOUD_...) fails
// here, which the SALAD-prefix scan above cannot see.
func TestEveryVariableTheCodeNamesFollowsTheRule(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	for _, path := range ruleFiles(t, root) {
		if filepath.Ext(path) != ".go" || filepath.Base(path) == "envrule_test.go" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range envCall.FindAllStringSubmatch(string(data), -1) {
			name := m[1]
			if systemEnv()[name] || sharedEnv()[name] || strings.HasPrefix(name, "SALAD_CLI_") {
				continue
			}
			t.Errorf("%s: %s is neither a shared SALAD_ name, SALAD_CLI_-prefixed, nor a system variable", path, name)
		}
	}
}
