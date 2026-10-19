// Package config handles the saladcloud config file and resolution of
// credentials/defaults from flags, environment variables, and the file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// File is the on-disk config document at Path().
type File struct {
	APIKey       string `json:"api_key,omitempty"`
	Organization string `json:"organization,omitempty"`
	Project      string `json:"project,omitempty"`
	// GatewayAPIKey is the AI Gateway key: a separate, organization-scoped
	// credential from the SaladCloud API key (see the AI Gateway section of docs/guide.md).
	GatewayAPIKey string `json:"ai_gateway_api_key,omitempty"`
}

// Path returns the config file location:
// [filepath.Join]([os.UserConfigDir](), "saladcloud", "config.json").
//
// If the SALAD_CLI_CONFIG_DIR environment variable is set, that directory is
// used instead of [os.UserConfigDir]()/saladcloud. This exists primarily so
// tests (and sandboxed environments) can redirect config I/O without
// touching the real user config.
func Path() (string, error) {
	if dir := os.Getenv("SALAD_CLI_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "config.json"), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "saladcloud", "config.json"), nil
}

// ReadError is a config file that exists but cannot be read or parsed.
type ReadError struct {
	Path    string
	Err     error
	Corrupt bool // read, but not a valid config document
}

// Error names the file when it is corrupt, and is the read error otherwise.
func (e *ReadError) Error() string {
	if e.Corrupt {
		// Every command loads the config, including `config set`, so a
		// corrupt file blocks everything; say what to do about it.
		return fmt.Sprintf("cannot read the config file %s: %v", e.Path, e.Err)
	}
	return e.Err.Error()
}

// Unwrap returns the read or parse error.
func (e *ReadError) Unwrap() error { return e.Err }

// Reason is why the file could not be read, without its path.
func (e *ReadError) Reason() string { return Reason(e.Err) }

// Reason is an error's cause without the path an os error wraps it in, and
// without a final period (Windows ends its messages with one).
func Reason(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return strings.TrimSuffix(err.Error(), ".")
}

// Load reads the config file. A missing file is not an error: it returns a
// zero File and nil error. An unreadable or corrupt file returns a
// *ReadError.
func Load() (File, error) {
	var f File
	p, err := Path()
	if err != nil {
		return f, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return File{}, nil
		}
		return File{}, &ReadError{Path: p, Err: err, Corrupt: false}
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, &ReadError{Path: p, Err: err, Corrupt: true}
	}
	f.Organization, f.Project = strings.TrimSpace(f.Organization), strings.TrimSpace(f.Project)
	return f, nil
}

// ErrNotFolder is a path on the way to the config file that exists but is
// not a folder.
var ErrNotFolder = errors.New("is not a folder")

// Writable reports why the config file could not be saved, or nil: the
// file, when it exists, opens for writing, and its folder (or the nearest
// folder above it that exists, which Save would create it in) takes a new
// file. Nothing it writes stays.
func Writable() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if f, openErr := os.OpenFile(p, os.O_WRONLY, 0); openErr == nil {
		if closeErr := f.Close(); closeErr != nil {
			return closeErr
		}
	} else if !errors.Is(openErr, os.ErrNotExist) {
		return openErr
	}
	dir := filepath.Dir(p)
	for {
		st, statErr := os.Stat(dir)
		if statErr == nil {
			if !st.IsDir() {
				return fmt.Errorf("%s %w", dir, ErrNotFolder)
			}
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	tmp, err := os.CreateTemp(dir, ".saladcloud-check-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := tmp.Close(); err != nil {
		return errors.Join(err, os.Remove(name))
	}
	return os.Remove(name)
}

// Save writes the config file, creating its directory (0700) if needed.
// The file itself is written with permission 0600.
func Save(f File) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// Write a temp file beside the config and rename it into place, so a
	// crash or a full disk never leaves a half-written (corrupt) config,
	// which would block every command.
	tmp, err := os.CreateTemp(filepath.Dir(p), ".config-*.json")
	if err != nil {
		return err
	}
	// On any failure the temp file goes; errors.Join keeps the failure's
	// message as it is unless the removal fails too.
	name := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return errors.Join(err, tmp.Close(), os.Remove(name))
	}
	if _, err := tmp.Write(data); err != nil {
		return errors.Join(err, tmp.Close(), os.Remove(name))
	}
	if err := tmp.Close(); err != nil {
		return errors.Join(err, os.Remove(name))
	}
	if err := os.Rename(name, p); err != nil {
		return errors.Join(err, os.Remove(name))
	}
	return nil
}

// Resolved is the effective configuration after applying precedence, with
// per-field provenance. A source is one of "flag", "env", "config", or ""
// (not set anywhere).
type Resolved struct {
	APIKey, APIKeySource               string
	Organization, OrganizationSource   string
	Project, ProjectSource             string
	GatewayAPIKey, GatewayAPIKeySource string
	// SkippedProject is the config file's project when it was not used
	// because it belongs to the file's organization (SkippedProjectOrg) and
	// another one is in effect; both "" otherwise.
	SkippedProject, SkippedProjectOrg string
}

// DefaultProject is the project assumed when none is set anywhere.
const DefaultProject = "default"

// SourceDefault is the provenance reported for a value that came from a
// built-in fallback rather than a flag, the environment, or the file.
const SourceDefault = "default"

// Resolve applies per-field precedence flag > env > config file.
// Environment variables: SALAD_API_KEY, SALAD_ORGANIZATION, SALAD_PROJECT.
// Missing values are never an error — the value and source just stay "" —
// with two refinements for the project. The config file's project belongs
// to the config file's organization, so it is not used when a flag or the
// environment names another organization (a project carried across
// organizations is a footgun). And when nothing sets a project it falls
// back to "default" (source "default"), because every organization is
// created with a project of that name; commands that use it say so on
// stderr, and a fallback that does not exist is reported as no valid
// project set. Commands decide what they require. The only error case is a
// present but unreadable/corrupt config file.
func Resolve(orgFlag, projectFlag string) (Resolved, error) {
	f, err := Load()
	if err != nil {
		return Resolved{}, err
	}
	return ResolveFrom(f, orgFlag, projectFlag), nil
}

// ResolveFrom is Resolve with the config file already read (or, when it
// cannot be read, an empty one: flags and the environment still resolve).
func ResolveFrom(f File, orgFlag, projectFlag string) Resolved {
	pick := func(flagVal, envName, fileVal string) (string, string) {
		if flagVal != "" {
			return flagVal, "flag"
		}
		if v := os.Getenv(envName); v != "" {
			return v, "env"
		}
		if fileVal != "" {
			return fileVal, "config"
		}
		return "", ""
	}
	var r Resolved
	r.APIKey, r.APIKeySource = pick("", "SALAD_API_KEY", f.APIKey)
	// The gateway docs name the variable SALAD_AI_GATEWAY_API_KEY; keep it.
	r.GatewayAPIKey, r.GatewayAPIKeySource = pick("", "SALAD_AI_GATEWAY_API_KEY", f.GatewayAPIKey)
	// Names are trimmed: a variable sourced from a Windows-saved file can
	// end in a carriage return. Keys are trimmed where they are used.
	name := func(flagVal, envName, fileVal string) (string, string) {
		flagVal, envVal, fileVal := strings.TrimSpace(
			flagVal,
		), strings.TrimSpace(
			os.Getenv(envName),
		), strings.TrimSpace(
			fileVal,
		)
		switch {
		case flagVal != "":
			return flagVal, "flag"
		case envVal != "":
			return envVal, "env"
		case fileVal != "":
			return fileVal, "config"
		}
		return "", ""
	}
	savedOrg := strings.TrimSpace(f.Organization)
	r.Organization, r.OrganizationSource = name(orgFlag, "SALAD_ORGANIZATION", savedOrg)
	fileProject := f.Project
	if fileProject != "" && savedOrg != "" && r.Organization != savedOrg {
		r.SkippedProject, r.SkippedProjectOrg, fileProject = fileProject, savedOrg, ""
	}
	r.Project, r.ProjectSource = name(projectFlag, "SALAD_PROJECT", fileProject)
	if r.Project != "" {
		r.SkippedProject, r.SkippedProjectOrg = "", ""
	}
	if r.Project == "" {
		r.Project, r.ProjectSource = DefaultProject, SourceDefault
	}
	return r
}
