# pact

`pact`, short for *pipelines-as-code tester*, runs your `.tekton/`
definitions locally, against your uncommitted working tree: no remote Git
forge, no webhooks, no GitHub App.

It is to Pipelines-as-Code what [nektos/act](https://github.com/nektos/act) is
to GitHub Actions: the feedback loop you wanted before pushing.

## How it works

A Pipelines-as-Code controller sits in your cluster and reacts to forge
webhooks. pact does that work locally: it synthesizes the webhook event from
your git state (branch, commit, changed paths), matches it against the
`.tekton/` annotations with PAC's own matcher, resolves the matched
PipelineRuns — hydrating `{{ }}` templates, without fetching remote tasks —
and submits them to a cluster, streaming task and step logs as they run.
Because the matching and resolution code is PAC's, what pact selects is what
the controller would have selected.

## Requirements

- Go 1.27+ and Docker

## Try it on a local kind cluster

pact's own repository is the demo: `.tekton/` holds seven Pipelines-as-Code
pipelines — unit tests, linters, a push build, comment-triggered e2e, a
label-gated full suite, a CEL-matched feature check and a docs link check —
and every one of them runs locally.

```bash
git clone https://github.com/theakshaypant/pact
cd pact && make install
```

### Start a local kind cluster with pipeline installation

```bash
pact up --provider kind
```

This creates a `pact-local` kind cluster in Docker and installs Tekton from a
manifest embedded in the binary, so it works offline. The command is
idempotent — an existing Tekton installation is left untouched and only
verified.

Keep passing `--provider kind` on every command: it makes pact target
`pact-local` specifically, rather than whatever cluster your current
kubeconfig context happens to point at.

### Open a pull request

The default event. Modify a `.go` file — the run executes against your
uncommitted working tree — then:

```bash
pact run --provider kind
```

pact synthesizes a pull_request against your branch's default target and
runs every matching pipeline — the unit tests and linters on Go changes, the
docs link check on markdown changes — streaming each step's logs live. The
simulated PR takes its title, sender and target branch from your repository;
override with `--pr-title`, `--sender` and `--target-branch`.

### Change only certain files

The same pull_request command — which pipelines fire depends on what you
have modified. Edit a `.go` file and the unit tests and linters run; edit a
`.md` file and the docs link check runs too. `pact match -v` prints the skip
reason for every pipeline that did not match.

### Push to main

```bash
pact run --provider kind --event push
```

Simulates a push of your current branch: the build pipeline compiles the
binary, passes its path to the smoke task as a Tekton result, and runs a
`finally` task either way.

### Label the pull request

```bash
pact run --provider kind --label ok-to-test
```

Simulates a labeled PR (`ok-to-test`, the PAC retest convention): the full
test suite runs regardless of which files changed. `--label` is repeatable.

### Comment on the pull request

```bash
pact run --provider kind --comment "/test-e2e"
```

Simulates a PR comment, matched against each pipeline's `on-comment` regex
and passed to the pipeline via `{{ trigger_comment }}`.

### Match on the pull request title

```bash
pact run --provider kind --pr-title "feat: demo"
```

Pipelines can match a CEL expression over the whole event; this one matches
feature-titled PRs and builds and smoke-tests the binary. The expression
sees the event type, branches, changed files and the parsed webhook body.

### Supply custom params

```bash
pact run --provider kind --param docs-root=.
```

The docs pipeline declares a custom param: `docs-root` scopes its link
check and defaults to the docs directory. `--param` overrides it,
substituting `{{ docs-root }}` in the manifest and landing in the
PipelineRun's `spec.params`. `--param` is repeatable and `--param-file`
reads a dotenv file of them.

### Clean up

```bash
pact down --provider kind
```

Run namespaces are deleted after a fully successful run and kept for
inspection, with a `kubectl` hint, when anything fails.

Before running anything, `pact match` with the same event flags (e.g.
`pact match --event push`) prints which pipelines an event would fire — add
`-v` for why the rest were skipped — and `pact resolve` prints the hydrated
YAML that would be submitted. Both run fully offline and need no cluster.
The full CLI reference — every command, flag, default and exit code — lives
in [docs/cli.md](docs/cli.md).
