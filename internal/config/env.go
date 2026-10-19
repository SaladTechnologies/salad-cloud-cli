package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// Env is the process environment as the CLI reads it: its variables and the
// operating system it runs on. Everything the CLI reads from the environment
// goes through one, so a test can hand it a map.
type Env struct {
	Getenv func(string) string
	GOOS   string
}

// ProcessEnv is the running process's environment.
func ProcessEnv() Env { return Env{Getenv: os.Getenv, GOOS: runtime.GOOS} }

// The errors ConfigDir and HomeDir return, worded as os.UserConfigDir and
// os.UserHomeDir word theirs.
var (
	errNoAppData       = errors.New("%AppData% is not defined")
	errNoHome          = errors.New("$HOME is not defined")
	errNoPlan9Home     = errors.New("$home is not defined")
	errNoUserProfile   = errors.New("%userprofile% is not defined")
	errNoConfigHome    = errors.New("neither $XDG_CONFIG_HOME nor $HOME are defined")
	errRelativeXDGHome = errors.New("path in $XDG_CONFIG_HOME is relative")
)

// ConfigDir is [os.UserConfigDir] computed from e: %AppData% on Windows,
// $HOME/Library/Application Support on macOS, $home/lib on Plan 9, and
// $XDG_CONFIG_HOME or $HOME/.config elsewhere.
func ConfigDir(e Env) (string, error) {
	switch e.GOOS {
	case "windows":
		dir := e.Getenv("AppData")
		if dir == "" {
			return "", errNoAppData
		}
		return dir, nil
	case "darwin", "ios":
		dir := e.Getenv("HOME")
		if dir == "" {
			return "", errNoHome
		}
		return dir + "/Library/Application Support", nil
	case "plan9":
		dir := e.Getenv("home")
		if dir == "" {
			return "", errNoPlan9Home
		}
		return dir + "/lib", nil
	default:
		dir := e.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			home := e.Getenv("HOME")
			if home == "" {
				return "", errNoConfigHome
			}
			return home + "/.config", nil
		}
		if !filepath.IsAbs(dir) {
			return "", errRelativeXDGHome
		}
		return dir, nil
	}
}

// HomeDir is [os.UserHomeDir] computed from e: %USERPROFILE% on Windows,
// $home on Plan 9, and $HOME elsewhere.
func HomeDir(e Env) (string, error) {
	name, missing := "HOME", errNoHome
	switch e.GOOS {
	case "windows":
		name, missing = "USERPROFILE", errNoUserProfile
	case "plan9":
		name, missing = "home", errNoPlan9Home
	}
	if v := e.Getenv(name); v != "" {
		return v, nil
	}
	switch e.GOOS {
	case "android":
		return "/sdcard", nil
	case "ios":
		return "/", nil
	}
	return "", missing
}
