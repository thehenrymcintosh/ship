# ship

`ship` runs your coding workflows with Claude Code, on your machine. You
describe a workflow once, for example "implement it, have it reviewed, run the
tests, open a PR, wait for it to merge", and `ship` runs it for each piece of
work you hand it. Agents do the writing and reviewing, scripts do the
checking, and you're asked only when a decision needs a person.

All of a run's work happens in its own git worktree, on its own branch, so
several runs can go side by side without touching your checkout, and you can
jump into any of them, see exactly what's been done, step in, or stop the
run and finish the job yourself. Runs survive crashes and reboots, and you
watch them live in a local web UI.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/thehenrymcintosh/ship/main/install.sh | sh
```

This installs the latest release for macOS or Linux (arm64/amd64) to
`~/.local/bin`, after checking it against the release checksums. Set
`SHIP_INSTALL_DIR` to install elsewhere, or `SHIP_VERSION=v0.2.0` to pin a
version.

To update:

```sh
ship update            # installs the latest release and restarts ship's background process onto it
ship update --check    # only says whether there's a newer one
```

You need git and the [`claude` CLI](https://docs.anthropic.com/en/docs/claude-code).
If [treehouse](https://github.com/kunchenguid/treehouse) is installed,
`ship` leases its worktrees from treehouse's pool instead of creating them
itself (see [Worktrees](#worktrees-jumping-in-and-taking-over)). To build from
source, see [Development](#development).

## Quick start

**1. Set up your repo.**

```sh
cd your-repo
ship init
```

This creates `.ship/` (where pipelines live) and installs three Claude Code
skills: `/ship-design`, `ship-handoff` and `ship-feedback`.

**2. Design a pipeline with Claude.** In Claude Code, run `/ship-design` and
say what you want in plain words:

```
/ship-design implement the change, have it reviewed, run the tests, then open a PR and wait for it to merge
```

It asks a few questions about your workflow, reads the plan back to you,
then writes `.ship/pipelines/<name>.yml` and checks it. It only runs when
you invoke it.

**3. Plan a change, then hand it over.** Talk the change through with Claude
as usual. When you're happy with the plan, say **"hand this to ship"**. The
`ship-handoff` skill writes a brief (the context, the plan and acceptance
criteria) and starts a run. The web UI opens on it.

**4. Watch, and step in when asked.** The run works in its own worktree. When
it needs you, for example an agent is stuck or the slices of a big change need
approving, it shows up in the UI's inbox (with a desktop notification on
macOS). Answer there or from the terminal.

**5. Tell it what you thought.** When a run's work was off (or great), say so:
in the run's page, in Claude Code ("tell ship the docs were bloated"), or in
a PR comment starting with `ship:`. Feedback builds up against the pipeline
version that did the work, and `ship pipeline refine` turns it into a better
version. See [Improving pipelines with feedback](#improving-pipelines-with-feedback).

### The web UI

`ship open` opens it (it starts automatically with your first run). It's
served only on `127.0.0.1` and signs your browser in with a local token.

- **Runs:** every run, grouped by repo, with its status, current step,
  elapsed time and cost. 🔔 marks runs waiting for you. Runs that were split
  into slices show their child runs underneath.
- **A run's page:**
  - The pipeline as a graph, with the current step highlighted and the
    paths taken so far drawn solid. Click a step to show only its visits.
  - A timeline of every step visit. Each one has the live output (script
    output, or the agent's messages and tool calls), the exact input it got,
    the handover it wrote for the next step, and its raw result.
  - When the run needs you: the question, any context it chose to show, a
    note box and one button per choice.
  - Controls to retry a step, jump to another step, resume an interrupted
    agent session, or cancel. You can also edit the run's variables, and
    copy the worktree path or open it in your editor or terminal.
- **Inbox:** everything waiting for you across all runs, oldest first,
  answerable in place.
- **Pipelines:** each repo's pipelines (and your global ones) as graphs,
  with any validation problems and a form to start a run.

### The CLI

Everything the UI does is also available in the terminal. Where a command
takes a run, any unique part of its id works (`3fa`, `rate-limit`).

| Command | What it does |
|---|---|
| `ship start --brief brief.md` | Start a run from a brief file (`-` reads stdin) |
| `ship ls` | Active runs (`--all` includes finished ones; `--pipelines` lists pipelines) |
| `ship status <run>` | Where a run is: current step, visits, pending question, worktree |
| `ship logs <run> -f` | Follow the current step's output |
| `ship answer <run> [choice] --note "…"` | Answer a question (prompts for the choice if you leave it out) |
| `ship pause <run>` / `ship resume <run>` | Hold a run after its current step, and carry on later (`--all` includes its slices) |
| `ship retry <run>`, `ship goto <run> <step>`, `ship cancel <run>` | Step in manually |
| `ship cd <run>` | Print the worktree path: `cd "$(ship cd 3fa)"` |
| `ship open [run]` | Open the web UI |
| `ship validate` | Check this repo's pipelines |
| `ship feedback <run> [--step s] "…"` | Record feedback on a run's work |
| `ship pipeline refine <pipeline>` | Propose a new version from the feedback |
| `ship templates` / `ship add <name>` | List and add pipeline templates |
| `ship init` / `ship update` | Set up a repo / update ship |

`ship <command> --help` has the details. A background process (the
"daemon") runs the pipelines; it starts by itself and keeps going after you
close the terminal. `ship serve --restart` restarts it.

### Example uses

Each of these is one `/ship-design` conversation away.

- **Fix it and get it reviewed.** An agent implements the change, a second
  agent reviews it and either passes it or sends it back with notes, then
  your tests run. If the reviewer is stuck, or the same step fails too many
  times, the run stops and asks you.
  > `/ship-design implement, review until it passes, then run make test. Ask me if it gets stuck.`
- **Take a change all the way to a merged PR.** As above, then push the
  branch, open a draft PR, have an agent review the PR, mark it ready, and
  wait. New review comments go back to an agent to address; a merge ends the
  run.
  > `/ship-design implement, review, test, open a PR, wait for review; address comments until it's merged`
- **Split a big feature into stacked PRs.** An agent splits the plan into
  small slices following your rules, you approve the split, then each slice
  runs the PR workflow on its own branch, each stacked on the one before. The
  next slice starts as soon as the previous one is waiting for review.
  > `/ship-design split the brief into stacked PRs following .ship/rules/splitting.md, then run my pr pipeline for each slice`
- **Run several independent changes at once.** Hand over several briefs, and
  each run gets its own worktree and branch. At most three agents work at
  once by default (`max_agents` in config); the rest queue.

## Worktrees: jumping in and taking over

Every run works in a separate git worktree, a full checkout of your repo on
the run's own branch (`ship/<run-id>` unless the pipeline names it). Agents,
scripts and your tests all run there. Your main checkout is never touched,
so you can keep working while runs go.

**Find it.** `cd "$(ship cd <run>)"` drops you into a run's worktree. On the
run's page in the UI, the worktree path has buttons to copy it or open it in
your editor or a terminal.

**See what's happened.** It's a normal git checkout, so the history and the
diff tell you everything:

```sh
cd "$(ship cd 3fa)"
git log --oneline main..       # what the run has committed
git diff main...               # everything it's changed so far
git status                     # anything not committed yet
```

For the story behind the changes, each step's handover says what it did and
what's left. They're on the run's page in the UI, via `ship status <run>`
and `ship logs <run> <step> --stream handover`, and on disk in
`~/.ship/state/runs/<id>/visits/`.

**Step in without stopping the run.** When a run is waiting for you (a
question, or a step that needs attention), you can edit, fix or commit in
its worktree first. When you answer or retry, the next step picks up your
changes. To redirect a run that's mid-step, `ship goto <run> <step>` stops
the current step and continues from the one you name, for example after you've
fixed something by hand, `ship goto 3fa test`. Avoid editing while an agent
is actively working there: you'll both be changing the same files.

**Pause it.** `ship pause <run>` (or **Pause** on the run's page) lets the
step that's running finish, then holds the run before the next one. Its
worktree and agent conversations stay exactly as they are, so you can look
around or make changes, then `ship resume <run>`. A paused run isn't in your
inbox and stays paused across restarts. For a run that's splitting work into
slices, pausing stops it starting new slices while the running ones carry
on. `--all` (**Pause all**) pauses those too.

**Stop it and take over.** `ship cancel <run>` (or **Cancel run** in the UI)
stops the run and leaves its worktree and branch exactly as they are. Carry
on from there yourself: finish the change, commit, push, open the PR. When
you're done with the worktree, `ship clean --run <run>` removes it (the
branch stays). `ship clean` refuses if there are uncommitted changes, unless
you pass `--force`.

**After a run.** A run that finishes successfully releases its worktree, but
its branch stays, so `git switch ship/<run-id>` (or `git log ship/<run-id>`)
in your checkout still gets you everything. Runs that stop, fail or are
cancelled keep their worktree for you to inspect, until `ship clean`.

### With treehouse

If [treehouse](https://github.com/kunchenguid/treehouse) is on your PATH,
`ship` uses it automatically. Instead of creating a worktree, each run
**leases** one from treehouse's pool of pre-warmed worktrees, on the run's
branch, so it starts faster. Your `treehouse.toml` decides where the pool
lives and what's set up in new worktrees.

- `treehouse status` lists the worktrees `ship` holds, with `ship:<run-id>`
  as the lease holder. Leased worktrees are never handed to anyone else or
  pruned.
- Everything above works the same: `ship cd` gives you the leased path.
- When a run finishes, `ship` returns the lease, and treehouse resets the
  worktree for reuse. Your commits stay on the run's branch. A cancelled,
  stopped or failed run keeps its lease, so you can take over, until
  `ship clean` returns it. Like treehouse itself, `ship clean` won't
  discard uncommitted work without `--force`.
- `ship` only ever returns leases it holds.

To choose explicitly, set `workspace.provider` in `.ship/config.yml`, in
`~/.ship/config.yml`, or in a pipeline: `auto` (the default: treehouse if
installed, otherwise git), `git`, `treehouse` (fails if treehouse is
missing), or `none` (work directly in your checkout, one run at a time).

### Editing pipelines while runs are in progress

You can edit pipelines at any time:

- **A run uses the pipeline as it was when the run started.** It keeps its own
  copy, so editing the file never changes a run that's already going.
- **Slices use the child pipeline as it is when each slice starts.** Edits
  made while a big feature is being built reach its later slices. If the file
  doesn't validate at that moment, the slice uses the copy taken when the
  parent started instead.
- **Repo skills, rules and scripts come from the run's branch.** Agents and
  scripts run in the worktree, so edits in your checkout reach runs started
  from a base that includes them.
- **Your user-level skills (`~/.claude/skills`) are read at every step.**
  Editing one changes the next agent step of every run in progress.

## Improving pipelines with feedback

You judge the work; `ship` keeps track of what you said and which version of
the pipeline it was about, and Claude helps you act on it.

**Versions are automatic.** A pipeline's version is a fingerprint of the
pipeline file, any pipelines it fans out to, every skill or slash command its
steps call (found in the repo's `.claude/skills/` or your `~/.claude/skills/`,
including all the skill's files), and the rules files and helper scripts it
uses. Each run records the version it used, computed from exactly what its
agents see: its own copy of the pipeline and its worktree's skills, rules and
scripts. When anything in that set
changes, whether you edited it or `refine` did, the next run is the next
version (v1, v2, …). `ship pipeline versions <pipeline>` lists them, with
what changed in each.

**Give feedback wherever you are.** It's stored against the version that
did the work, optionally about one step:

- The run's page in the web UI has a feedback box.
- In Claude Code, just say it: "tell ship the PR description didn't explain
  why", "give ship feedback: the review missed the race condition". The
  `ship-feedback` skill finds the run and step and records it.
- On a PR a run opened, start a comment with `ship:`, e.g.
  `ship: tests only cover the happy path`. `ship` picks these up every few
  minutes (it needs the `gh` CLI). Other review comments are left alone:
  they're instructions for the agents.
- From the terminal:

  ```sh
  ship feedback 3fa "the architecture was right, but the writing was weak"
  ship feedback 3fa --step docs "too long, and no examples"
  ship feedback --pipeline pr "reviews keep missing error handling"
  ```

Positive feedback is worth giving too; it tells a refinement what to keep.

**Refine.** When feedback has built up:

```sh
ship pipeline feedback pr      # what's open
ship pipeline refine pr        # propose a new version
```

Claude reads the open feedback, the pipeline and the skills, rules and
scripts it uses, and proposes changes. Each change says which feedback it
addresses, and anything it decided not to act on is listed with a reason. It
can only change the pipeline's own files, or add new ones under `.ship/` or
a skills folder. You see the diff, then **apply** it, **keep** it to make the
changes yourself, or **discard** it. Applying records the new version as
addressing that feedback. If you fix something by hand instead, mark the
feedback done with `ship pipeline close pr 4 7`.

**See whether it's working.** `ship pipeline report pr` has Claude read all
the feedback version by version, relative to how many runs each version had.
It reports the recurring themes, what changed after each version (including
regressions), and what's most worth fixing next. It analyses your judgements;
nothing grades the work automatically.

Everything lives in `.ship/history/<pipeline>/` (`versions.jsonl`,
`feedback.jsonl`, saved proposals and reports), beside the pipeline, so
commit it with the pipeline and the reasoning behind each version travels
with it. Global pipelines keep theirs in `~/.ship/history/`.

## Briefs

A run starts from a brief: markdown with a little YAML at the top. The
handoff skill writes these for you; you can also write one yourself.

```markdown
---
title: Rate limit the public API          # required
pipeline: pr                              # which pipeline (optional if there's only one)
vars: { ticket: API-123 }                 # values for the pipeline's variables
acceptance:
  - Requests over the limit get 429 with Retry-After
  - Limits are configurable per API key
---

## Context
Why this matters, constraints, links.

## Plan
The agreed approach, key decisions, files involved.
```

Every agent step is told to read the brief, and sees the acceptance
criteria.

## Writing pipelines by hand

`/ship-design` writes pipelines for you, but they're plain YAML files you can
read and edit. They live in `.ship/pipelines/<name>.yml`; the file name is
the pipeline's name. `ship init` sets up VS Code (`.vscode/settings.json`)
and JetBrains IDEs (`.idea/jsonSchemas.xml`) to autocomplete and check them
against the schema.

### A complete example

```yaml
# yaml-language-server: $schema=../schema/pipeline.json
version: 1
description: Implement, review and test a change
start: implement

defaults:
  max_visits: 3            # a step entered more often than this…
  when_exhausted: check-in # …goes here instead
  on_error: check-in       # where crashes, timeouts and bad results go

steps:
  implement:
    prompt: Implement the plan in the brief. Commit your work.
    session: builder       # a named conversation (see "Sharing an agent between steps")
    next: review           # one target for every outcome

  review:
    description: Code review
    prompt: Review the changes on this branch against the brief.
    model: opus
    next:                  # the agent picks one of these outcomes
      pass: test
      changes: implement
      stuck: check-in

  test:
    run: make test         # exit 0 → pass, anything else → fail
    next: { pass: done, fail: implement }

  check-in:
    ask: "{{brief.title}} stopped at {{came_from}}. What next?"
    show: [prev.handover]
    choices:               # button label → where it goes
      retry: $came_from
      rework: implement
      abandon: stop
```

A run starts at `start`, and each step's outcome picks the next step through
`next`. `done` ends the run successfully; `stop` abandons it. Every step
writes a **handover** (what happened and what's left), which the next step
receives, so context flows along the pipeline.

### Step types

Each step has exactly one of these:

| Step | What it does | Its outcomes |
|---|---|---|
| `agent:` or `prompt:` | Runs Claude Code in the run's worktree. `agent:` is a skill line such as `/review mode=code`; `prompt:` is free text; you can use both. | The keys of `next`: the agent chooses one and explains why in its handover. With a single `next` target, the only outcome is `done`. |
| `run:` | Runs a bash script in the worktree. | `pass` (exit 0) or `fail`. Map exit codes yourself with `outcomes: {0: pass, 2: flaky, default: fail}`. |
| `ask:` | Pauses for a person. Uses `choices:` instead of `next:`. `input: none \| optional \| required` controls the note box. | The label of the button pressed. The note becomes the handover. |
| `wait:` | Polls a command every `every` (default `1m`) until the last line it prints matches a key of `next`. `timeout` defaults to `24h`. | The keys of `next`, plus `timeout` if you map it. |
| `split:` | An agent splits the brief into slices, following a `rules:` file. `review: true` pauses for your approval. | `ok` (required) plus any others you add, such as `unclear`. |
| `fanout:` | Runs another pipeline once per slice: `mode: series` (`stack: true` bases each slice's branch on the previous one; `advance_on: <step>` starts the next slice when this one reaches that step) or `mode: parallel` (`max_parallel`). | `done` when every slice finished, otherwise `failed`. |

### Routing, retries and errors

- **Targets** in `next` and `choices` are a step name, `done`, `stop`, or
  `$came_from` (back to the step that led here; useful for a "retry" button).
- **Errors.** Every step can also produce `error` (a crash, timeout, invalid
  agent result or missing variable). Send it somewhere with `on_error:` (per
  step, or in `defaults`). Otherwise the run pauses and appears in your inbox.
- **Loops.** `max_visits` caps how often a step runs in one run, so
  fix-and-review loops can't spin forever. When the cap is hit the run goes
  to `when_exhausted`, or pauses. Choosing an option in an `ask` step resets
  the counter of the step it leads to.

### Sharing an agent between steps

By default every agent step starts a fresh Claude conversation: it gets the
brief and the previous step's handover, and re-reads whatever code it needs.
That keeps steps independent, but rebuilding context costs tokens. `session:`
lets steps share a conversation instead:

```yaml
steps:
  domain:
    prompt: Design the domain model.
    session: design        # starts the "design" conversation
    next: review
  review:
    prompt: Review the domain model.   # fresh: an independent reviewer
    next: interface
  interface:
    prompt: Design the interfaces on top of it.
    session: design        # resumes it: already knows the domain model
    next: verify
  verify:
    prompt: Check everything works end to end.
    session: continue      # its own conversation, resumed on each revisit
    next: {pass: done, again: fix}
```

- `fresh` (the default): a new conversation every visit.
- `continue`: the step's own conversation, resumed each time the run comes
  back to it. Good for a verifier that re-checks later, or an implementer in
  a fix loop.
- Any other name: steps with the same name share one conversation. The first
  to run starts it; each later one resumes it with its own prompt and
  outcomes. Good for steps that build on each other: domain design then
  interface design, writing docs then gathering PR evidence.

A resumed agent is told what happened since its last turn: the steps that ran
in between (with their summaries), the commits made since, and the files
that changed, including uncommitted ones. On the run's page, a ↻ beside a
visit marks a resumed conversation.

Keep checking steps separate from the work they check: a reviewer that shares
the implementer's conversation inherits its blind spots. `ship validate`
warns (W107) when a step can send work back to a step it shares a session
with. Long shared conversations also grow with every step, so sharing pays
off for closely related steps rather than whole pipelines.

### Variables

```yaml
variables:
  ticket: { from_brief: true, format: '^[A-Z]+-[0-9]+$' }  # from the brief's vars: (or --var)
  test:   { value: make test }                             # fixed
  pr_url: { set_by: open-pr }                              # saved by a step
  owner:  { ask: "Who should review this?" }               # asked the first time it's used
```

A `run:` step saves output with `save: { pr_url: last_line }` (or `stdout`,
`file:<path>`, `json:<dotted.path>`). An agent step lists the variables it
must return: `save: [pr_title]`.

### Placeholders

Prompts, scripts, questions and branch names can use `{{vars.ticket}}`,
`{{brief.title}}`, `{{brief.path}}`, `{{run.branch}}`, `{{run.base}}`,
`{{run.worktree}}`, `{{prev.summary}}`, `{{came_from}}`, and in slice
pipelines `{{slice.title}}`, `{{slice.number}}` and `{{parent.vars.ticket}}`.

In `run:` and `wait:` scripts each value is inserted as one safely quoted
word. To run a variable as a command, write `{{raw vars.test}}`. Scripts
also get environment variables such as `SHIP_RUN_ID`, `SHIP_BRANCH` and
`SHIP_VAR_TICKET`.

### Agents and worktrees

```yaml
agent:                       # defaults for every agent step (each can override)
  model: sonnet              # sonnet | opus | haiku | a full model id
  effort: medium             # low | medium | high | xhigh | max
  permission_mode: acceptEdits
  allowed_tools: ["Bash(make *)"]

workspace:
  branch: "feat/{{vars.ticket}}"   # default: ship/<run-id>
  base: main                       # default: the repo's default branch
  provider: auto                   # auto | git | treehouse | none (see Worktrees)
```

Agents run unattended, in the worktree, with your project's
`.claude/settings.json`, skills and `CLAUDE.md`. Tools they're refused are
flagged in the UI. Add what they need to `allowed_tools`.

Put helper scripts in `.ship/bin/` and supporting docs (like splitting rules)
in `.ship/rules/`. They run from the worktree, so they're versioned with
your code.

### Checking and trying a pipeline

```sh
ship validate                 # errors and warnings, with file:line:col
ship graph <pipeline>         # the flow as a Mermaid diagram
```

To try a pipeline without spending on real agents, script what each agent
step should answer and start a run with `--fake-agents`. Visit N of a step
uses entry N; the last entry repeats.

```yaml
# fake.yml
implement: [{outcome: done, summary: "wrote the code"}]
review:
  - {outcome: changes, summary: "missing tests"}
  - {outcome: pass, summary: "looks good"}
```

```sh
ship start <pipeline> --brief brief.md --fake-agents fake.yml
```

## Templates

`ship templates` lists ready-made pipelines and `ship add <name>` copies one
into `.ship/`, along with any helper scripts, rules and agent skills it
uses. Files you've already edited are kept unless you pass `--force`.

Templates are built into `ship` (none yet; they'll be added as pipelines
prove themselves) or your own, in `~/.ship/templates/<name>/`. Yours win if
the names clash. A template is a folder:

```
<name>/
  template.yml        # description: one line shown by `ship templates`
  pipelines/*.yml     # → .ship/pipelines/
  bin/*               # → .ship/bin/ (made executable)
  rules/*             # → .ship/rules/
  skills/<skill>/     # → .claude/skills/<skill>/
```

`/ship-design` can save a pipeline you've designed as a template.

## Global pipelines

Pipelines in `~/.ship/pipelines` work in every repo. If a repo has a pipeline
with the same name, the repo's wins.

```sh
ship init --global            # sets up ~/.ship, and installs the skills for your user
ship add <template> --global
ship ls --pipelines           # shows whether each pipeline comes from the repo or is global
```

Global pipelines still run inside each repo's worktree, so their helper
scripts belong in `~/.ship/bin`, called as `"$SHIP_HOME/bin/<script>"`.

## Where things live

| Path | What |
|---|---|
| `<repo>/.ship/` | pipelines, helper scripts, rules, config (commit these) |
| `<repo>/.ship/history/<pipeline>/` | the pipeline's versions, feedback, refinement proposals and reports (commit these) |
| `<repo>/.claude/skills/ship-*` | the Claude Code skills |
| `~/.ship/state/runs/<id>/` | everything about a run: its event log, brief, and each step's input, output and handover |
| `~/.ship/pipelines/`, `~/.ship/templates/` | global pipelines and your templates |
| `~/.ship/config.yml` | user config (`.ship/config.yml` overrides it per repo) |
| `~/.ship/daemon.log` | the background process's log |
| `<repo-parent>/<repo>.ship/<run-id>/` | run worktrees, when they're not leased from treehouse (released when a run finishes successfully; `ship clean` tidies up the rest) |

## Development

Build and install your local changes (Go 1.23+):

```sh
make install                  # → ~/.local/bin/ship
ship serve --restart          # if the daemon is running, switch it to the new build
```

Tests:

```sh
make test         # unit, golden, end-to-end with fake agents, daemon integration
make test-race
make test-live    # one real Claude call with haiku (costs a few cents)
make schema       # regenerate schema/*.json after changing the pipeline structs
```

### Publishing a release

```sh
scripts/release.sh                  # patch bump; or minor, major, or an exact X.Y.Z
scripts/release.sh minor --dry-run  # show what would happen
```

The script checks you're on a clean, up-to-date `main`, runs the tests,
builds and installs the new version locally, then tags and pushes. The
pushed tag triggers the release workflow, which publishes the downloads
that `install.sh` and `ship update` use.
