# langdag-lab

Builds real [langdag](https://langdag.com) conversation trees against mock
providers and captures them as deterministic JSON fixtures.

The fixtures are read by the `/demos/langdag-lab` page on
[pigfox.com](https://pigfox.com). The page renders what langdag actually
produces — node shapes, sequence numbers, provider attribution, token counters —
rather than a hand-drawn imitation of it.

## Why the capture lives here

The website could have built these trees at render time. Importing the full
langdag client pulls 52 modules and 357 third-party packages, including an AWS
credential chain and a Google Cloud auth stack, in order to draw a diagram.

So the capture runs here, where that dependency belongs, and only the JSON
travels. The website imports `langdag.com/langdag/types` alone — one `require`
line, a two-line `go.sum`, and no third-party package at all.

## What is captured

Three scenarios, one fixture each, in `fixtures/`.

| Scenario | Slug | What it shows |
| --- | --- | --- |
| Tool round trip | `tool-use` | The model asks for a tool, the caller answers, the model uses the answer. Four nodes, because langdag does not run tools — it stores the request as content blocks and hands control back. |
| Two continuations of one node | `branch-compare` | One node continued twice with the *same* follow-up and answered differently each time. Both keep the whole path above them. |
| Weighted policy, preferred route declines | `weighted-fallback` | A policy sending 80% to one deployment and 20% to a standby. The preferred route answers, then declines, and the standby picks it up. Each answer carries the name of the deployment that served it. |

Nothing contacts a network and no credential is read. Every answer is scripted
in `harness/scenario.go`, where you can read the whole conversation in one
place.

## No database file

langdag ships a SQLite store and its `New` constructor creates one on disk.
This repository uses `langdag.NewWithDeps(store, provider)` with an in-memory
store instead, so a capture leaves nothing behind. A database written during a
capture would be a second source of truth beside the JSON, and two is one more
than can be kept honest.

## Determinism

langdag stamps a fresh UUID on every node, a second one on every output group,
a wall-clock timestamp, and a measured latency. None of those describe the
conversation, and all of them change between two runs of the same code.

`harness/canon.go` projects them away — ids become ordinals (`n0`, `n1`, …) in
walk order, groups become `g0`, `g1`, …, and timestamps and latencies are
zeroed. Everything else passes through, including the model resolution and
pricing snapshot langdag reads from the catalog embedded in the pinned module.

The risk in a projection like that is doing too much, not too little: one that
flattened enough would make any two conversations compare equal and report
success while proving nothing. So every rule is tested in **both** directions —
a re-run's different UUIDs must not read as a difference, and a moved node, a
renamed provider, a changed token count or an edited word must.

Sibling order is decided by hashing the whole subtree, not the node. The
two-continuation scenario produces two user nodes identical in sequence, type
and content; only their descendants differ, and a node-only comparison would
call them ambiguous and refuse a tree that is perfectly well determined.

## Commands

```
go run . capture     build every scenario and write its fixture
go run . verify      re-run every capture and compare with what is committed
./test.sh            the whole gate
```

`verify` is the point of a captured fixture. A committed JSON file is a claim
that a program produced those exact bytes, and nothing about the file makes the
claim true — one edited by hand looks identical to one that was captured.

## The gate

`./test.sh` runs gofmt, `go vet`, the suite under `-race` with shuffled
ordering, coverage, and the verify step.

Coverage is an **equality at 100% of statements** on `./harness`, not a floor.
A floor drifts downward one honest exception at a time. At 100 the only way to
add uncovered code is to argue for it out loud — which is why the error paths
here are driven by a fault-injecting store rather than left as branches nobody
has seen run, and why two checks that could not fail were deleted instead of
excused. `package main` is not measured; it is three lines and a `func main`
cannot be entered by `go test` at all.

## Version pin

`go.mod` pins `langdag.com/langdag v0.10.0` exactly, and `harness/constants.go`
restates that version so every fixture carries its own provenance. `CheckPin`
holds the two together, so an upgrade cannot leave three fixtures claiming a
version that no longer built them.

## Licence

MIT — see [LICENSE](LICENSE). Copyright (c) 2026 Pigfox LLC.

langdag itself is MIT, copyright its authors, and is not vendored here.
