package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// The whole of facet#138 in one table: three answers that used to be exit 1,
// and the two of them that were never findings.
//
// The middle column is what a caller can act on. Without it, argano's console
// has to declare "exit 1 from facet tree doctor means findings" -- and then
// reads a 404 as one.
func TestTreeDoctorDistinguishesFindingsFromNotHavingLooked(t *testing.T) {
	withRouting(t, labelledFourLevelStructure)

	cases := []struct {
		name string
		// build returns the error the doctor produced for this case.
		build func(t *testing.T) error
		want  int
	}{
		{
			name: "a real defect: looked, and here is what is wrong",
			build: func(t *testing.T) error {
				t.Helper()
				f := wireFake()
				// A closed parent holding an open child -- wrong on any tree's
				// own terms, so this needs no structure to be a finding.
				f.issues["acme/lab#46"] = &ghx.Issue{Title: "commission 1", State: "CLOSED"}
				f.children = map[string][]ghx.IssueRef{
					"acme/lab#46": {iref("acme", "doctrine", 282)},
				}
				return runTreeDoctor(&bytes.Buffer{}, f, iref("acme", "lab", 46), false)
			},
			want: exitLooked,
		},
		{
			name: "the walk failed: nothing was read at all",
			build: func(t *testing.T) error {
				t.Helper()
				f := wireFake()
				delete(f.issues, "acme/lab#46") // ViewIssue answers "no such issue"
				return runTreeDoctor(&bytes.Buffer{}, f, iref("acme", "lab", 46), false)
			},
			want: exitCantLook,
		},
		{
			name: "an HTTP error reading the children",
			build: func(t *testing.T) error {
				t.Helper()
				f := wireFake()
				f.childErrs = map[string]error{
					"acme/lab#46": errors.New("repos/acme/lab/issues/46: HTTP 404"),
				}
				return runTreeDoctor(&bytes.Buffer{}, f, iref("acme", "lab", 46), false)
			},
			// !! THIS CASE EXPECTED exitLooked UNTIL facet#147, AND THAT WAS
			// THE DEFECT. !!
			//
			// The property it was written to protect is UNCHANGED and still
			// asserted elsewhere: the walk does not fail on an unreadable
			// child, it records the node and keeps going, so the rest of the
			// tree still answers, and the node is still named in the output.
			// What was wrong was calling that a FINDING -- exit 1 means "I
			// looked, and here is what is wrong", and nobody looked at this
			// node.
			//
			// Measured live under a GraphQL exhaustion on 2026-08-11: a run
			// against a node whose read did not answer printed "1 defect(s)"
			// and exited 1, from the verb whose own --help tells callers not to
			// read a could-not-look as a finding.
			want: exitCantLook,
		},
		{
			name: "the root itself is unreachable",
			build: func(t *testing.T) error {
				t.Helper()
				f := wireFake()
				f.viewErrs = map[string]error{
					"acme/lab#46": errors.New("repos/acme/lab/issues/46: HTTP 404"),
				}
				return runTreeDoctor(&bytes.Buffer{}, f, iref("acme", "lab", 46), false)
			},
			want: exitCantLook,
		},
		{
			name: "a --fix-labels write that failed",
			build: func(t *testing.T) error {
				t.Helper()
				f := wireFake()
				f.children = map[string][]ghx.IssueRef{
					"acme/lab#46": {iref("acme", "doctrine", 282)},
				}
				f.addLabelErr = errors.New("failed to update: 'type/seat' not found")
				return runTreeDoctor(&bytes.Buffer{}, f, iref("acme", "lab", 46), true)
			},
			want: exitCantLook,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.build(t)
			if err == nil {
				t.Fatal("doctor succeeded; this case is supposed to fail")
			}
			if got := exitCodeFor(err); got != tc.want {
				t.Errorf("exit code = %d, want %d\n  error: %v", got, tc.want, err)
			}
		})
	}
}

// !! AN HONEST EXIT CODE MUST NOT SWALLOW THE FINDINGS. !!
//
// `tree doctor` brackets every write to the commission tree -- run before and
// after, where the "after" is what catches the defect just introduced. Exiting
// 2 because some node could not be read is correct; printing nothing about the
// defects it DID find would cost the bracket the thing it exists for.
func TestDoctorStillPrintsTheDefectsWhenItExitsCouldNotLook(t *testing.T) {
	withRouting(t, labelledFourLevelStructure)
	f := wireFake()
	// A real, readable defect: a closed parent holding an open child.
	f.issues["acme/lab#46"] = &ghx.Issue{Title: "commission 1", State: "CLOSED"}
	f.children = map[string][]ghx.IssueRef{
		"acme/lab#46": {iref("acme", "doctrine", 282), iref("acme", "harness", 121)},
	}
	// And one node nobody could read.
	f.childErrs = map[string]error{
		"acme/doctrine#282": errors.New("repos/acme/doctrine/issues/282: HTTP 404"),
	}

	var out bytes.Buffer
	err := runTreeDoctor(&out, f, iref("acme", "lab", 46), false)
	if err == nil {
		t.Fatal("doctor reported a tree with an unread node as clean")
	}
	if got := exitCodeFor(err); got != exitCantLook {
		t.Errorf("exit code = %d, want %d (could not look)\n  error: %v", got, exitCantLook, err)
	}
	printed := out.String()
	if !strings.Contains(printed, "is closed, but") {
		t.Errorf("the real defect was not printed:\n%s", printed)
	}
	if !strings.Contains(printed, "COULD NOT LOOK") {
		t.Errorf("the unread node is not under its own heading:\n%s", printed)
	}
	// And the two must not be conflated in the count a human reads.
	if !strings.Contains(err.Error(), "not a verdict on the tree") {
		t.Errorf("the error does not say the report is not a verdict:\n%v", err)
	}
}

// A clean tree is still exit 0, which is the half a code change like this
// quietly breaks.
func TestTreeDoctorCleanTreeStillExitsZero(t *testing.T) {
	withRouting(t, "")
	f := wireFake()
	f.children = map[string][]ghx.IssueRef{}

	if err := runTreeDoctor(&bytes.Buffer{}, f, iref("acme", "lab", 46), false); err != nil {
		t.Fatalf("clean tree reported an error: %v", err)
	}
}

// A malformed reference is the case the issue measured first, and it never
// reaches runTreeDoctor -- so it has to be tagged at the command boundary.
func TestTreeDoctorRefusesABadRefWithoutClaimingAFinding(t *testing.T) {
	cmd := newTreeDoctorCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"not-a-ref"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal(`"not-a-ref" was accepted as an issue reference`)
	}
	if got := exitCodeFor(err); got != exitCantLook {
		t.Errorf("exit code = %d, want %d (could not look)\n  error: %v", got, exitCantLook, err)
	}
}

// The wrong number of arguments is not a finding about any tree either, and
// cobra produces that error rather than this package.
func TestTreeDoctorArgumentErrorIsNotAFinding(t *testing.T) {
	cmd := newTreeDoctorCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(nil)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("doctor ran with no argument")
	}
	if got := exitCodeFor(err); got != exitCantLook {
		t.Errorf("exit code = %d, want %d\n  error: %v", got, exitCantLook, err)
	}
}

// So is a mistyped flag, which cobra reports through its own error path.
func TestTreeDoctorFlagErrorIsNotAFinding(t *testing.T) {
	cmd := newTreeDoctorCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--fix-lables", "acme/lab#46"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("a misspelled flag was accepted")
	}
	if got := exitCodeFor(err); got != exitCantLook {
		t.Errorf("exit code = %d, want %d\n  error: %v", got, exitCantLook, err)
	}
}

// THE DEFAULT IS WHAT MAKES THIS ADDITIVE. Every other command still exits 1
// on any error, so nothing that reads facet's exit codes today changes.
func TestUntaggedErrorsStillExitOne(t *testing.T) {
	if got := exitCodeFor(errors.New("some other command failed")); got != 1 {
		t.Errorf("exit code = %d, want 1", got)
	}
}

// A tagged error must survive being wrapped, or the tag is only as good as the
// last caller that reformatted the message.
func TestTheCodeSurvivesWrapping(t *testing.T) {
	err := fmt.Errorf("while checking the tree: %w", withCode(exitCantLook, errors.New("HTTP 404")))
	if got := exitCodeFor(err); got != exitCantLook {
		t.Errorf("exit code = %d, want %d", got, exitCantLook)
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("wrapping lost the message: %q", err.Error())
	}
}

// withCode must not manufacture an error out of a nil one, since it is applied
// straight to a call's result.
func TestWithCodeLeavesNilAlone(t *testing.T) {
	if err := withCode(exitCantLook, nil); err != nil {
		t.Errorf("withCode(nil) = %v, want nil", err)
	}
}

// The codes are in --help. A caller that must not read 2 as a finding has to
// be able to find that out without reading this source file.
func TestTreeDoctorHelpStatesTheExitCodes(t *testing.T) {
	long := newTreeDoctorCmd().Long
	for _, want := range []string{"EXIT CODES", "could NOT look", "1  looked", "2  could"} {
		if !strings.Contains(long, want) {
			t.Errorf("--help is missing %q:\n%s", want, long)
		}
	}
}
