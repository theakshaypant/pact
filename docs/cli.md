# pact CLI reference

Every command, flag, default and exit code. For a walkthrough, see the
[README](../README.md).

## Global flags

Inherited by every subcommand.

| Flag | Default | Description |
| --- | --- | --- |
| `-C, --dir <path>` | `.` | Repository directory to operate on; a subdirectory is fine. |
| `--tekton-dir <name>` | `.tekton` | Pipelines-as-Code YAML directory, relative to the repository root. |
| `-v, --verbose` | off | Verbose logging on stderr. |

## Commands

No command takes positional arguments.

| Command | [Event flags](#event-flags) | [Param flags](#param-flags) | [Cluster flags](#cluster-flags) | Own flags |
| --- | --- | --- | --- | --- |
| [`match`](#match) | yes | – | – | – |
| [`resolve`](#resolve) | yes | yes | – | – |
| [`run`](#run) | yes | yes | yes | `--workspace`, `--workspace-size`, `--keep-clone-tasks`, `--no-cleanup` |
| [`up`](#up) | – | – | yes | `--tekton-version` |
| [`down`](#down) | – | – | yes | `--force` |

### match

Takes the [event flags](#event-flags).

Prints every `.tekton` file with a pipeline matching the simulated
event, one per line, each file once. No match: empty output, exit 0.
`-v` adds skip reasons. Offline.

### resolve

Takes the [event flags](#event-flags) and [param flags](#param-flags).

Prints the hydrated PipelineRun YAML — placeholders substituted, local
tasks and pipelines inlined, params applied — as a multi-document stream
on stdout. No match: error. Remote taskRefs are not fetched; they error.
Offline.

### run

Takes the [event flags](#event-flags), [param flags](#param-flags) and
[cluster flags](#cluster-flags).

| Flag | Default | Description |
| --- | --- | --- |
| `--workspace <name>` | – | Pipeline workspace to bind the source to. Default: the one named `source`, else the single declared workspace. |
| `--workspace-size <size>` | `1Gi` | Size of the source workspace volume. |
| `--keep-clone-tasks` | off | Do not drop repository-fetching tasks. |
| `--no-cleanup` | off | Keep the run namespace even when every run succeeded. |
| `--timeout <duration>` | `30m` | Maximum time to wait for the cluster and the runs. |

Matches and resolves first; no match prints `no matching pipelines` and
exits 0 without touching a cluster. Otherwise ensures the cluster and
Tekton (like `pact up`), packages the repository — respecting
`.gitignore` — into a `pact-run-<hex>` namespace with one shared source
volume, and submits the runs sequentially, streaming logs as
`[task : step]`, one color per task.

- `--workspace` must name a declared workspace. A pipeline declaring no
  workspace, or several with neither `source` nor an override: error.
- Repository-fetching tasks and steps (`git-clone`,
  `fetch-repository`, `checkout`, ...) are dropped and their `runAfter`
  references pruned; a remaining reference to one errors (hint:
  `--keep-clone-tasks`). A `pipelineRef`-only PipelineRun: error.
- Outcome per run: `name: succeeded` / `name: failed: <message>`; any
  failure exits 1.
- Namespace: deleted after full success or a setup failure before the
  first submit; kept when a submitted run fails, on error or
  cancellation after the first submit, or with `--no-cleanup`.

### up

Takes the [cluster flags](#cluster-flags).

| Flag | Default | Description |
| --- | --- | --- |
| `--tekton-version <version>` | – | Install this official Tekton release (e.g. `v1.19.0`) instead of the embedded manifest; requires network. Must match `vMAJOR.MINOR.PATCH`. |
| `--timeout <duration>` | `10m` | Maximum time to wait for the cluster and Tekton. |

Idempotent: an existing cluster is reused, an existing Tekton
installation is left untouched and only verified — unless
`--tekton-version` is passed, which installs the given release
regardless. The manifest is resolved before the cluster is created; a
bad version leaves no cluster behind.

### down

Takes the [cluster flags](#cluster-flags).

| Flag | Default | Description |
| --- | --- | --- |
| `--force` | off | Remove a Tekton installation even if pact did not apply it (external only). |
| `--timeout <duration>` | `5m` | Maximum time to wait for the teardown. |

External: removes only the Tekton installation pact applied — the
cluster keeps running. A Tekton installation that predates pact is left
in place and errors; pass `--force` to remove it. Kind: deletes the
whole `pact-local` cluster.

## Event flags

Shared by match, resolve and run.

| Flag | Default | Description |
| --- | --- | --- |
| `--event <type>` | `pull_request` | Simulated GitHub event type: `pull_request` or `push`; anything else errors. |
| `--target-branch <name>` | – | Target branch of the event; for `--event push`, only the base of the diff. Auto-detected: `origin/HEAD`, then `main`, then `master`. |
| `--sender <login>` | – | Sender login of the event. |
| `--pr-number <n>` | `1` | Pull request number of the event. |
| `--pr-title <text>` | – | Pull request title. |
| `--label <name>` | – | Label on the simulated pull request (repeatable). |
| `--comment <text>` | – | Comment body, matched by `on-comment` regexes. |

Defaults come from git state: the pull request runs from the current
branch to the target branch, its title and sender from the HEAD commit
(sender falls back to `pact`), its changed paths from the committed diff
against the target branch plus the uncommitted working tree. Push
targets the current branch; the "before" SHA is the merge base.

`--label` and `--comment` refine a pull request into the labeled and
comment kinds, matching `on-label` and `on-comment` annotations; the
comment body reaches the pipeline as `{{ trigger_comment }}`. Labels and
comments are pull-request-only and cannot be combined. Labels are
trimmed; an empty label, or an explicitly empty or whitespace-only
`--comment`, errors.

Non-GitHub origin (neutral GitHub fields) and detached HEAD (literal
branch `HEAD`) warn on stderr. No detectable target branch: error, pass
`--target-branch`.

## Param flags

Shared by resolve and run.

| Flag | Default | Description |
| --- | --- | --- |
| `--param <name=value>` | – | Custom param (repeatable; a later flag wins). |
| `--param-file <path>` | – | Dotenv file of custom params; `--param` overrides it on the same name. |

Custom params stand in for PAC's Repository CR params. Each value
substitutes `{{ name }}` anywhere in the `.tekton` manifests and is
merged into every matched PipelineRun's `spec.params` — Tekton accepts
undeclared params, so `resolve` previews exactly what `run` submits.

- A `--param` value splits at the first `=`: trimmed name, verbatim
  value (may be empty, may contain `=`). Missing `=`, empty name, or an
  explicitly empty `--param-file`: error.
- Names must match `[_a-zA-Z][_a-zA-Z0-9-]*`, must not shadow a
  built-in (`revision`, `repo_url`, ...) and must not start with `body`,
  `headers` or `files` — PAC routes those through CEL, so they would
  never substitute.
- A param no matched pipeline declares or references errors before any
  cluster work — the typo check.

The param file is dotenv-style:

```
environment=prod
docs-root=docs
greeting="hello, world"
```

`#` comments and blank lines skipped; CRLF and a UTF-8 BOM handled; one
pair of surrounding quotes stripped; later line wins; no escapes, inline
comments or `export`. A line without `=`, or with an empty name, errors
with its file and line number. The path is relative to the working
directory, not `--dir`.

## Cluster flags

Shared by run, up and down. `--timeout` belongs to the group but keeps
its own default per command (run 30m, up 10m, down 5m), so each
command's table lists it.

| Flag | Default | Description |
| --- | --- | --- |
| `--provider <name>` | `external` | `external` connects to a pre-existing cluster and never creates or deletes it; `kind` has pact create and own a `pact-local` cluster in Docker, reusing it when it exists. Anything else errors. |
| `--kubeconfig <path>` | – | Kubeconfig of the cluster to connect to; implies `--provider external`. Combining it with an explicitly requested other provider errors. |

External kubeconfig resolution: `--kubeconfig`, then the first
`$KUBECONFIG` entry, then `~/.kube/config`.

## Environment variables

| Variable | Effect |
| --- | --- |
| `KUBECONFIG` | Kubeconfig discovery for the external provider; see [Cluster flags](#cluster-flags). |
| `NO_COLOR` | Any non-empty value disables the log colors; they are also off when stdout is not a terminal. |

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success, including `match` and `run` with no matching pipelines. |
| 1 | Any error: flag validation, `resolve` with no match, failed PipelineRuns under `run`. |

Errors print one `pact: <message>` line to stderr. `SIGINT` and
`SIGTERM` cancel the running command.
