package main

import "golang.org/x/text/language"

// gateDemo calls a known-vulnerable function from a reachable path so
// govulncheck has a live symbol to find. Deliberate, and reverted in the
// following commit.
func init() { _ = gateDemo("en-US") }

func gateDemo(s string) bool {
	_, _, err := language.ParseAcceptLanguage(s)
	return err == nil
}
