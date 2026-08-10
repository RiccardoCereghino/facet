package main

import "errors"

// Exit codes facet uses. Only `tree doctor` distinguishes them today; every
// other command exits 1 on any error, which is what it has always done.
//
// THE DISTINCTION EXISTS BECAUSE ONE CODE CANNOT CARRY TWO ANSWERS. A checker
// that exits 1 for "I looked, and here is what is wrong" and 1 for "I never
// managed to look" leaves a caller with nothing but the prose on stderr to tell
// them apart -- and a classifier built on another tool's prose is one release
// note away from silently answering the wrong thing. That is not hypothetical:
// argano's console had to declare "exit 1 from facet tree doctor means
// findings", which reads a 404 as a finding (facet#138).
//
// `gad hold --fleet --check` already answers this way -- 1 while held, 2 when
// unreadable -- so this is the shape the surrounding tooling expects rather
// than a new convention.
const (
	exitLooked   = 1 // looked, and found something to report
	exitCantLook = 2 // could NOT look: bad ref, HTTP error, unreadable config
)

// codedError carries the exit code a command wants, so the decision is made
// where the two cases are actually distinguishable -- inside the command that
// knows whether it read anything -- rather than inferred later from a message.
type codedError struct {
	code int
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

// withCode tags err with an exit code. A nil error stays nil, so it is safe to
// wrap a call's result directly.
func withCode(code int, err error) error {
	if err == nil {
		return nil
	}
	return &codedError{code: code, err: err}
}

// exitCodeFor reads the code an error asks for, defaulting to 1.
//
// The default is what makes this additive: an error from any command that has
// not opted in exits exactly as it did before.
func exitCodeFor(err error) int {
	var c *codedError
	if errors.As(err, &c) {
		return c.code
	}
	return exitLooked
}
