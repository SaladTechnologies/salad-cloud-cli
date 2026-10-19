package config

import (
	"errors"
	"os"
	"testing"
)

// TestDirsMatchTheStandardLibrary: on the machine running the tests, the
// directories computed from the process's environment are the ones the os
// package finds.
func TestDirsMatchTheStandardLibrary(t *testing.T) {
	t.Parallel()
	wantConfig, wantConfigErr := os.UserConfigDir()
	gotConfig, gotConfigErr := ConfigDir(ProcessEnv())
	if gotConfig != wantConfig || (gotConfigErr == nil) != (wantConfigErr == nil) {
		t.Errorf("ConfigDir = %q, %v; os.UserConfigDir = %q, %v", gotConfig, gotConfigErr, wantConfig, wantConfigErr)
	}
	wantHome, wantHomeErr := os.UserHomeDir()
	gotHome, gotHomeErr := HomeDir(ProcessEnv())
	if gotHome != wantHome || (gotHomeErr == nil) != (wantHomeErr == nil) {
		t.Errorf("HomeDir = %q, %v; os.UserHomeDir = %q, %v", gotHome, gotHomeErr, wantHome, wantHomeErr)
	}
}

// TestDirsPerOS: each operating system's rule, from a fixed environment.
func TestDirsPerOS(t *testing.T) {
	t.Parallel()
	vars := map[string]string{
		"AppData": `C:\Users\u\AppData\Roaming`, "USERPROFILE": `C:\Users\u`,
		"HOME": "/home/u", "home": "/usr/u",
	}
	envOf := func(goos string, extra map[string]string) Env {
		return Env{GOOS: goos, Getenv: func(k string) string {
			if v, ok := extra[k]; ok {
				return v
			}
			return vars[k]
		}}
	}
	tests := []struct {
		name, goos    string
		extra         map[string]string
		wantConfig    string
		wantConfigErr error
		wantHome      string
		wantHomeErr   error
	}{
		{"windows", "windows", nil, `C:\Users\u\AppData\Roaming`, nil, `C:\Users\u`, nil},
		{"darwin", "darwin", nil, "/home/u/Library/Application Support", nil, "/home/u", nil},
		{"plan9", "plan9", nil, "/usr/u/lib", nil, "/usr/u", nil},
		{"linux", "linux", nil, "/home/u/.config", nil, "/home/u", nil},
		{"linux with XDG", "linux", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, "/xdg", nil, "/home/u", nil},
		{
			"linux with relative XDG",
			"linux",
			map[string]string{"XDG_CONFIG_HOME": "xdg"},
			"",
			errRelativeXDGHome,
			"/home/u",
			nil,
		},
		{"linux without HOME", "linux", map[string]string{"HOME": ""}, "", errNoConfigHome, "", errNoHome},
		{
			"windows without AppData",
			"windows",
			map[string]string{"AppData": "", "USERPROFILE": ""},
			"",
			errNoAppData,
			"",
			errNoUserProfile,
		},
		{"android without HOME", "android", map[string]string{"HOME": ""}, "", errNoConfigHome, "/sdcard", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := envOf(tt.goos, tt.extra)
			if got, err := ConfigDir(env); got != tt.wantConfig || !errors.Is(err, tt.wantConfigErr) {
				t.Errorf("ConfigDir = %q, %v; want %q, %v", got, err, tt.wantConfig, tt.wantConfigErr)
			}
			if got, err := HomeDir(env); got != tt.wantHome || !errors.Is(err, tt.wantHomeErr) {
				t.Errorf("HomeDir = %q, %v; want %q, %v", got, err, tt.wantHome, tt.wantHomeErr)
			}
		})
	}
}
