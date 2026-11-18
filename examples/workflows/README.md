# Example workflows

Runnable workflow definitions, one behaviour each. Every file is a complete definition in the format
its extension names — `.yaml`/`.yml`, `.json`, `.toml` — and `run start --file` selects the decoder
from the extension, so the same workflow can be written any of the three ways. These files are also
the fixtures the test suites load; the same bytes live under
[`test/testdata/workflows/`](../../test/testdata/workflows/).

Configure a provider first (see [README § Point it at a model](../../README.md#point-it-at-a-model)):

```bash
go build -o bin/zamnius ./cmd/zamnius
export ZAMNIUS_MODEL_API_KEY=sk-…           # the key is never read from a definition
# export ZAMNIUS_MODEL_BASE_URL=http://localhost:8080/v1   # any OpenAI-compatible endpoint
```

| File | Behaviour it demonstrates |
|---|---|
| [`hello.yaml`](hello.yaml) | The smallest run: one `single`-mode phase that declares a `plan` output. |
| [`hello.json`](hello.json) | The same workflow as JSON — one definition, three formats. |
| [`hello.toml`](hello.toml) | The same workflow as TOML — the third format. |
| [`fanout.yaml`](fanout.yaml) | A `fanout` phase: three branches joined by `all`, then a phase that `depends_on` the join. |
| [`slow.yaml`](slow.yaml) | A two-branch fan-out built for interruption drills: start it, `kill -9` mid-branch, resume. |
| [`budget-tokens.yaml`](budget-tokens.yaml) | A run-level token budget with a retry policy — the budget-stop path. |
| [`invalid.yaml`](invalid.yaml) | A definition the schema refuses, so validation exits `5`. |

## `hello.yaml` — one phase, one artifact

A single phase, `greet`, in `mode: single`, declaring `outputs: [plan]`. The run succeeds once a
`plan` artifact has committed, and `run start` reports `1 phase, 1 attempt, 1 artifact` with exit `0`.
Start here. (The README reproduces this file and its transcript in
[§ Your first run](../../README.md#your-first-run).)

## `hello.json` and `hello.toml` — the same workflow, three formats

The same `hello` workflow written as JSON and as TOML. Decoding is strict in every format — an
unknown field is refused and a duplicated key in one JSON object is an error, not a silent
last-one-wins — and the three files decode to one definition, so their runs are equivalent (same
phase count, same artifact kinds).

## `fanout.yaml` — fan out, then join

A `mode: fanout` phase, `review`, with three branches — `correctness`, `security`, `performance` —
joined by `join.rule: all`, and a second `mode: single` phase, `summarise`, that declares
`depends_on: [review]` and so starts only after the join commits. The join writes one `aggregate`
artifact listing the branch results in declared order; the run then produces a `report`
(`2 phases, 4 attempts, 5 artifacts`). `join.rule` accepts `all`, `any`, or `n_of_m`. The README
reproduces this file in [§ Fan out, then join](../../README.md#fan-out-then-join).

## `slow.yaml` — interrupt, then resume

A two-branch fan-out (`outline`, `details`, joined by `all`) whose branches are described as slow and
deliberate, so there is time to interrupt it mid-flight. Start it, `kill -9` the process while a
branch is in flight, then resume from a fresh process: the committed branch is never repeated, the
interrupted attempt is abandoned with a recorded reason, and only the unfinished branch restarts.
This is the fixture behind the drill in
[§ Interrupt it, then resume](../../README.md#interrupt-it-then-resume).

## `budget-tokens.yaml` — stop on the budget

Declares a run-level budget, `budget.tokens: 40`, and a phase retry policy, `retry.max_attempts: 3`.
The engine checks the budget before a phase starts; when the spend crosses it the phase cannot start,
a `phase.budget_exhausted` event names `tokens`, and the phase's failure policy applies — the default
is `on_failure: abort`, which fails the run with exit `3`. A provider whose single call stays under
40 tokens leaves the budget intact, so the same file also runs as an ordinary workflow; the
budget-stop path is asserted in `test/integration` and `test/secrets`.

## `invalid.yaml` — a refused definition

`mode: parallel` is not in the schema, so the definition is refused before any run is created:
`run start` exits `5` and the hint names the offending field path.

```console
$ bin/zamnius run start --file examples/workflows/invalid.yaml
zamnius: … workflow definition is invalid …
hint: phases[0].mode: must be "single" or "fanout", got "parallel"
$ echo $?
5
```

The whole accepted shape — every field, its type, and the `single`/`fanout` constraints — is in
[`workflow-definition.schema.json`](../../specs/001-multi-agent-framework/contracts/workflow-definition.schema.json).
