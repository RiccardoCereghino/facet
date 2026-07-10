// Package privacy holds a repository-wide guard, exercised only by its test.
//
// facet knows nothing about any particular organisation: routing tables and
// hazard fragments are data, supplied from outside the binary. The guard checks
// that no employer's names have crept into the source -- most easily via a test
// fixture copied from real data, which happened twice while this was written.
//
// The words it looks for are deliberately NOT in this repository. A public repo
// carrying a tidy list of an employer's internal system names would be the very
// leak the guard exists to prevent. Supply them from outside; see the test.
package privacy
