// Package ptr provides small pointer helpers for the SDK's pointer-heavy API.
package ptr

// To returns a pointer to v.
func To[T any](v T) *T { return &v }

// Deref returns *p, or the zero value of T if p is nil.
func Deref[T any](p *T) T { //nolint:ireturn // returns the caller's type parameter, not an interface
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// DerefOr returns *p, or def if p is nil.
func DerefOr[T any](p *T, def T) T { //nolint:ireturn // returns the caller's type parameter, not an interface
	if p == nil {
		return def
	}
	return *p
}
