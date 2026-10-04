# ship

`ship` runs local agentic-coding pipelines. A pipeline is a YAML file in your
repo whose steps are agent runs (Claude Code), shell scripts, human
check-ins and polls. Each step's outcome decides which step runs next. Every
run works in its own git worktree, survives crashes and reboots, and shows up
live in a local web UI.

## Install

```sh
make build            # → bin/ship (static, CGO_ENABLED=0)
```

You need Go 1.23+ to build, git, and the `claude` CLI for agent steps.
[treehouse](https://github.com/kunchenguid/treehouse) is optional.

## Quick start

```sh
cd your-repo
ship init                # .ship/ with starter pipelines, rules, schemas, plus the Claude handoff skill
ship validate            # errors and warnings with file:line:col
```

Write a brief (markdown with front matter) and start a run:

```markdown
---
title: Rate limit the public API
pipeline: feature
vars: { ticket: API-123 }
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

Or, at the end of a Claude Code planning chat, say "hand this to ship".
`ship init` installs the `ship-handoff` skill in `.claude/skills/`. It
writes the brief from the conversation and starts the run.

To design or change a pipeline without learning the YAML, run
`/ship-design` in Claude Code, followed by what you want, e.g.
`/ship-design implement, review, run tests, then open a PR and wait for it to merge`.
It asks about your workflow, describes the pipeline back in plain words,
writes the file, and checks it with `ship validate`. It only runs when you
invoke it. (`--no-skill` skips both skills.)

If the repo is a JetBrains project, `ship init` also maps the schemas in
`.idea/jsonSchemas.xml`, so pipeline YAML gets completion and checking
there too.

## Global pipelines

Pipelines in `~/.ship/pipelines` are available in every repo. A repo
pipeline with the same name wins.

```sh
ship init --global       # starter pipelines in ~/.ship/pipelines, skills in ~/.claude/skills
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
