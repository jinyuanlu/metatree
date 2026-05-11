package command

import "fmt"

// ExitError carries an exit code through error returns. main.go uses
// errors.As to recover the code; bare errors map to exit 1.
//
// See CONTRIBUTING.md §6 (Errors and exit codes). Standard codes:
//
//	0  — success (no error)
//	1  — generic mt error
//	2  — usage error (unknown subcommand, bad flags)
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// ExitWith creates an ExitError with the given code and formatted message.
// Replaces bash's `die "msg"; exit $?` pattern.
func ExitWith(code int, format string, args ...any) error {
	return &ExitError{Code: code, Err: fmt.Errorf(format, args...)}
}
