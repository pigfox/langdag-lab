#!/usr/bin/env bash
#
# test.sh — the repository's whole gate. Offline by design; there is no CI.
#
# THERE IS NO WORKFLOW FILE. Everything here runs in milliseconds against mock
# providers and an in-memory store, so a hosted runner would add latency and a
# second place for the answer to differ without adding a single assertion. The
# gate runs where the capture runs.
#
# WHAT IT ENFORCES, in order, cheapest first:
#
#   1. formatting        — the harness is real Go and is held to it
#   2. go vet            — the standard suspicious-construct pass
#   3. the test suite    — with the race detector, because the store is
#                          concurrency-safe and a claim like that is worth
#                          checking rather than asserting
#   4. COVERAGE AT 100%  — see below
#   5. the captures      — re-run and byte-compared against what is committed
#
# THE COVERAGE GATE IS AN EQUALITY, NOT A FLOOR, and that is the point. A
# threshold like "at least 90" drifts downward one honest exception at a time
# and nobody notices the day it stopped meaning anything. At 100 the only way to
# add uncovered code is to argue for it out loud.
#
# PACKAGE main IS NOT MEASURED, and the exclusion is stated here rather than
# buried. A func main cannot be entered by go test at all, so including it would
# put a permanent ceiling under 100 and force the gate down to a floor. main.go
# is three lines and delegates immediately; everything it delegates to is in
# ./harness and is measured.
#
# STEP 5 IS WHY THIS REPOSITORY EXISTS. A committed fixture is a claim that a
# program produced those exact bytes, and nothing about a JSON file makes that
# claim true — one edited by hand looks identical to one that was captured. The
# verify step is what turns the claim into something checkable.
set -u

cd "$(dirname "$0")" || exit 1

# The one number this file is about.
readonly WANT="100.0%"

# The package under measurement. Named once so the test run and the coverage run
# cannot drift onto different scopes.
readonly MEASURED="./harness/"

fail() {
    echo
    echo "GATE FAILED: $*"
    exit 1
}

echo "==> gofmt"
# -l lists files that are not formatted; any output at all is a failure.
fmt_out="$(gofmt -l .)"
[ -z "$fmt_out" ] || fail "not gofmt-clean:
$fmt_out"

echo "==> go vet"
go vet ./... || fail "go vet had something to say"

echo "==> go test -race"
# Shuffled so an ordering dependency between tests shows up here rather than as
# a mystery on someone else's machine.
go test -race -shuffle=on ./... || fail "the test suite is red"

echo "==> coverage (must be exactly $WANT of statements on $MEASURED)"
profile="$(mktemp)"
trap 'rm -f "$profile"' EXIT

go test -covermode=set -coverprofile="$profile" "$MEASURED" \
    || fail "coverage run did not complete"

go tool cover -func="$profile" | tail -n 20

got="$(go tool cover -func="$profile" | awk '$1 == "total:" { print $NF }')"
[ -n "$got" ] || fail "no total row in the coverage report; the format moved"

echo
echo "    statements $got"

if [ "$got" != "$WANT" ]; then
    echo
    echo "  uncovered:"
    # count 0 in the final column of a profile line is an unexecuted block.
    awk -F'[:, ]' '$NF == 0' "$profile" | sed 's/^/    /'
    fail "coverage is $got, and this gate requires $WANT.
    Add the missing test, or — if a branch genuinely cannot be reached — change
    the code so the branch does not exist. Both of those are improvements; an
    exception is not."
fi

echo "==> fixtures reproduce"
go run . verify || fail "the committed fixtures are not what the harness produces.
    Re-run 'go run . capture' and read the diff: either the harness changed, or
    a fixture was edited by hand."

echo
echo "GATE PASSED: formatted, vet-clean, tests green under -race, coverage $WANT, fixtures reproduce."
