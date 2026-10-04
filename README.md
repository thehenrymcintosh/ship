# ship

`ship` runs local agentic-coding pipelines. A pipeline is a YAML file in your
repo whose steps are agent runs (Claude Code), shell scripts, human
check-ins and polls. Each step's outcome decides which step runs next. Every
run works in its own git worktree, survives crashes and reboots, and shows up
live in a local web UI.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/thehenrymcintosh/ship/main/install.sh | sh
```

This installs the latest release for macOS or Linux (arm64/amd64) to
`~/.local/bin`, after checking it against the release checksums. Set
`SHIP_INSTALL_DIR` to put it elsewhere, or `SHIP_VERSION=v0.2.0` to pin a
version.

To update later:

```sh
ship update            # latest release; restarts the daemon onto it
ship update --check    # just say whether there's a newer one
```

You need git, and the `claude` CLI for agent steps. To build from source
instead (Go 1.23+): `make build`, which writes `bin/ship`.

### Publishing a release

```sh
scripts/release.sh            # patch bump; or minor, major, or an exact X.Y.Z
scripts/release.sh minor --dry-run
```

It checks you're on a clean, up-to-date `main`, runs the tests, builds and
installs the new version locally, then tags and pushes. The pushed tag
triggers the release workflow, which publishes the archives `install.sh`
and `ship update` download.

To use your local changes without releasing: `make install` (then
`ship serve --restart` if the daemon is running).
[treehouse](https://github.com/kunchenguid/treehouse) is optional.

## Quick start

```sh
cd your-repo
ship init     # .ship/, schemas for your editor, and two Claude Code skills
```

`ship init` doesn't add any pipelines. Make one of these two ways:

- **Describe it.** In Claude Code, run `/ship-design` followed by what you
  want, e.g. `/ship-design implement, review, run tests, then open a PR and
  wait for it to merge`. It asks about your workflow, reads the plan back in
  plain words, writes `.ship/pipelines/<name>.yml` and checks it with
  `ship validate`. It only runs when you invoke it.
- **Start from a template.** `ship templates` lists them; `ship add <name>`
  copies one in, along with any scripts, rules and agent skills it uses.

Then start a run from a brief (markdown with front matter):

```markdown
---
title: Rate limit the public API
pipeline: my-pipeline
acceptance:
  - Requests over the limit get 429 with Retry-After
---
## Context
…
## Plan
…
```

```sh
ship start --brief brief.md     # starts the daemon if needed and opens the UI
ship ls                         # active runs (🔔 = waiting for you)
ship status 3fa                 # any unique part of a run id works
ship answer 3fa retry --note "try the other approach"
ship logs 3fa -f
cd "$(ship cd 3fa)"             # jump into the run's worktree
```

Or, at the end of a Claude Code planning chat, say "hand this to ship". The
`ship-handoff` skill writes the brief from the conversation and starts the
run. (`ship init --no-skill` skips both skills.)

`ship init` maps the pipeline schema for VS Code (`.vscode/settings.json`)
and, if the repo is a JetBrains project, in `.idea/jsonSchemas.xml`, so
pipeline YAML gets completion and checking.

## Templates

A template is a folder with a `template.yml` (`description: …`) and any of
`pipelines/`, `bin/`, `rules/` and `skills/<skill>/`. `ship add` copies those
into `.ship/` and `.claude/skills/` (or into `~/.ship` and `~/.claude/skills`
with `--global`), keeping files you've already edited unless you pass
`--force`.

Templates come from two places: ones built into `ship` (none yet: they get
added as pipelines prove themselves in practice, under
`internal/templates/library/`), and your own in `~/.ship/templates/<name>/`,
which win on a name clash. `/ship-design` can save a pipeline you've
designed as a template.

## Global pipelines

Pipelines in `~/.ship/pipelines` are available in every repo. A repo
pipeline with the same name wins.

```sh
ship init --global       # ~/.ship and user-level skills in ~/.claude/skills
ship add <template> --global
ship ls --pipelines      # shows each pipeline's source: repo or global
```

Global pipelines still run inside the repo's worktree. Shared helper
scripts go in `~/.ship/bin` and are called as `"$SHIP_HOME/bin/…"`.

## Pipelines at a glance

```yaml
# yaml-language-server: $schema=../schema/pipeline.json
version: 1
start: implement
defaults: { max_visits: 3, when_exhausted: check-in, on_error: check-in }
steps:
  implement:
    agent: /implement          # a skill, or prompt: "…"
    session: continue          # resume the same Claude session on revisits
    next: review
  review:
    agent: /review
    next: { pass: gate, changes: implement, stuck: check-in }   # the agent picks one
  gate:
    run: make test             # exit 0 → pass, else fail
    next: { pass: done, fail: implement }
  check-in:
    ask: "Stopped at {{came_from}}. What next?"
    choices: { retry: $came_from, abandon: stop }
```

There are also `wait` steps (poll a command until it prints an outcome),
`split` (an agent splits the brief into slices), and `fanout` (one child run
per slice, in series with stacked branches or in parallel). Every field is
documented in the JSON Schema (`ship schema`), which also drives editor
autocomplete.

## Where things live

| Path | What |
|---|---|
| `<repo>/.ship/` | pipelines, rules, helper scripts, config (committed) |
| `~/.ship/state/runs/<id>/` | events.jsonl (the source of truth), run.json, brief, visit logs and handovers |
| `~/.ship/pipelines/` | global pipelines |
| `~/.ship/daemon.json`, `daemon.log` | the running daemon (127.0.0.1, token-protected) |
| `<repo-parent>/<repo>.ship/<run-id>` | worktrees (git provider) |

## Development

```sh
make test         # unit, golden, engine e2e (fake agents), daemon integration
make test-race
make test-live    # one real Claude call with haiku (a few cents)
make schema       # regenerate schema/*.json after changing pipeline structs
```

Agent steps can be scripted for tests and dry runs with
`ship start --fake-agents script.yml`. Visit N of a step uses entry N (the
last entry repeats):

```yaml
implement: [{outcome: done, summary: "wrote code", sleep: 1s}]
review:
  - {outcome: changes, summary: "missing tests"}
  - {outcome: pass, summary: "lgtm"}
```
