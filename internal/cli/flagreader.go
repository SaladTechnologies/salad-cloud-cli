package cli

import (
	"time"

	"github.com/spf13/pflag"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// flagReader reads a command's flag values and keeps the first error. A read
// fails only for a flag the command does not define or one of another type,
// a bug rather than user input, so a function reads its flags in a row and
// checks Err once. The embedded FlagSet keeps Changed and Lookup at hand; the
// value methods hide FlagSet's flag-defining methods of the same names.
type flagReader struct {
	*pflag.FlagSet
	err error
}

// readFlags returns a flagReader over fs.
func readFlags(fs *pflag.FlagSet) *flagReader { return &flagReader{FlagSet: fs, err: nil} }

// keep records err when it is the first.
func (r *flagReader) keep(err error) {
	if r.err == nil {
		r.err = err
	}
}

// Err returns the first read error as the CLI's error, wrapped as wrapErr
// wraps any other error, or nil.
func (r *flagReader) Err() *clierr.Error {
	if r.err == nil {
		return nil
	}
	return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "%v", r.err)
}

// String returns the value of the string flag name.
func (r *flagReader) String(name string) string {
	v, err := r.GetString(name)
	r.keep(err)
	return v
}

// Bool returns the value of the bool flag name.
func (r *flagReader) Bool(name string) bool {
	v, err := r.GetBool(name)
	r.keep(err)
	return v
}

// Int returns the value of the int flag name.
func (r *flagReader) Int(name string) int {
	v, err := r.GetInt(name)
	r.keep(err)
	return v
}

// Int64 returns the value of the int64 flag name.
func (r *flagReader) Int64(name string) int64 {
	v, err := r.GetInt64(name)
	r.keep(err)
	return v
}

// Duration returns the value of the duration flag name.
func (r *flagReader) Duration(name string) time.Duration {
	v, err := r.GetDuration(name)
	r.keep(err)
	return v
}

// StringArray returns the values of the repeatable string flag name.
func (r *flagReader) StringArray(name string) []string {
	v, err := r.GetStringArray(name)
	r.keep(err)
	return v
}
