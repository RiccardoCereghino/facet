package main

import "golang.org/x/text/language"

// gateDemo calls a known-vulnerable symbol from a reachable path, to show the
// govulncheck gate bites. Deliberate, and reverted in the following commit.
func init() { _ = gateDemo("en-US") }

func gateDemo(s string) bool {
	_, _, err := language.ParseAcceptLanguage(s)
	return err == nil
}
