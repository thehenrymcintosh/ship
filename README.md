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

`ship update` also updates the skills `ship init` installed, for your user
and in every repo you've run ship in. Copies you've edited are kept
(`ship init --force` in a repo replaces them).

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
skills: `/ship-design`, `/ship` and `ship-feedback`.

**2. Design a pipeline with Claude.** In Claude Code, run `/ship-design` and
say what you want in plain words:

```
/ship-design implement the change, have it reviewed, run the tests, then open a PR and wait for it to merge
```

It asks a few questions about your workflow, reads the plan back to you,
then writes the pipeline as a folder, `.ship/pipelines/<name>/`, with
`pipeline.yml` and any skills written for it, and checks it. It only runs
when you invoke it.

**3. Plan a change, then hand it over.** Talk the change through with Claude
as usual. When you're happy with the plan, say **"ship this"** (or "ship
it", or run `/ship`). The `ship` skill writes a brief (the context, the plan
and acceptance criteria) and starts a run on your default pipeline, or asks
which one if you haven't set a default (`ship pipeline default <name>`). The
web UI opens on it. (Older versions called this skill `ship-handoff`;
`ship init` and `ship update` replace it.)

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
    paths taken so far drawn solid. Click a step (on any graph) to see what it
    does: its prompt or script, settings and where each outcome goes; on a run
    it also lists the step's visits and filters the timeline to them.
  - A timeline of every step visit. Each one has the live output (script
    output, or the agent's messages and tool calls), the exact input it got,
    the handover it wrote for the next step, and its raw result.
  - When the run needs you, one card that says first whether **something
    broke** (amber) or a **decision** is wanted (blue), with the pipeline,
    the step it came from, how long it has waited and what the run has cost.
    Then a one-line headline of what's being decided, the situation in two
    or three lines, a highlighted recommendation with its reason, and the
    options as buttons, each saying where it leads and what that step has
    cost per visit so far. The evidence (review findings, the full handover,
    errors) is folded away below. The card also says what changed since you
    last opened the run (new commits, visits, rounds of the step), which
    your browser remembers.
    - The headline and recommendation come from the agent: when its
      outcome leads to a check-in, it's asked for a `decision` (headline,
      situation, what each choice does, the one it recommends and why).
      When it gives none, haiku writes one from its handover (marked as
      generated; `checkins.summarize: false` in the config turns this off;
      it costs a cent or so, counted in the run's cost).
    - Known failures are diagnosed without an agent, each with a one-click
      fix: a denied tool offers **Allow `Bash(go test *)` for this
      pipeline** (it shows what it will write to the pipeline file, writes
      it keeping your comments, moves the run onto the edited pipeline and
      runs the step again); a timeout offers a higher one; a budget offers
      raise-and-retry; a usage limit says when it resets; a missing worktree
      offers re-acquire; a failing script lists the failing tests from its
      output.
    - Review findings written as `R1 [warning, auto-fix] file:line: …` show
      as cards coloured by severity, and the ones the reviewer left to you
      (`ask-user`) are highlighted, each with its own box for your call;
      those are added to your note. The note is for the next step by default
      (it becomes the check-in's handover, which that step reads); switch it
      to **for the whole run** and every later agent step of the run, and of
      any slices it splits into, gets it too. Those notes are listed under
      "Notes for this run" on the run's page.
  - What the run has cost so far, in dollars and tokens, against its budget
    if it has one. When it stops at its budget, you can raise it and retry
    in place.
  - What the run has spent in Claude's current 5-hour usage window, and a
    **Focus** button that pauses your other runs so this one can finish.
  - Controls to retry a step, jump to another step, resume an interrupted
    agent session, or cancel. You can also edit the run's variables, and
    copy the worktree path or open it in your editor or terminal.
- **Inbox:** everything waiting for you across all runs, oldest first,
  as the same cards, so a routine check-in can be answered with **Do
  recommended** without opening the run. Runs waiting for you are marked
  Something broke or Decision in the runs list too.
- **Usage meter:** the top bar shows how much of Claude's 5-hour and weekly
  usage limits your account has used (as of the last agent step), and the
  runs page breaks the current window down by run, with advice when several
  runs are racing for what's left.
- **Pipelines:** each repo's pipelines (and your global ones) as graphs,
  with any validation problems and a form to start a run.
- **A pipeline's page:** how it's doing across its runs and versions:
  success, time, cost, how hands-on runs were, each step's numbers with
  suggestions, its versions, and whether the newest version is better than
  the last. See [Stats](#stats-and-whether-a-new-version-is-better).
- **Charts** show their details on hover, keyboard focus or tap: a
  histogram bar's range, count and share of runs (per version when two are
  overlaid), a box plot's n, min, quartiles, median and max, a graph step's
  visits and outcomes, a route's count, a usage meter's use and reset time.
  Table rows that sum up several values (time, cost and tokens per step or
  per version) have a small histogram of them alongside.

### The CLI

Everything the UI does is also available in the terminal. Where a command
takes a run, any unique part of its id works (`3fa`, `rate-limit`).

| Command | What it does |
|---|---|
| `ship start --brief brief.md` | Start a run from a brief file (`-` reads stdin) |
| `ship start --brief b.md --after <run> [--stack]` | Queue a run behind another: it waits (no worktree, no agent) until that run is done, then starts from its base as it is then, or from that run's branch with `--stack`. If that run ends any other way, you're asked whether to start anyway |
| `ship start-now <run>` / `ship start-now <run> --slice N` | Start a waiting run now, or a pending slice of a run that's running its slices (out of turn, past `max_parallel`) |
| `ship ls` | Active runs (`--all` includes finished ones; `--pipelines` lists pipelines) |
| `ship status <run>` | Where a run is: current step, visits (with time, cost and tokens), worktree, and the check-in card when it waits for you (recommendation first) |
| `ship logs <run> -f` | Follow the current step's output |
| `ship answer <run> [choice] --note "…" [--for run]` | Answer a question (without a choice it shows the card and prompts, Enter taking the recommended one). The note goes to the next step; `--for run` sends it to every later agent step too |
| `ship pause <run>` / `ship resume <run>` | Hold a run after its current step, and carry on later (`--all` includes its slices) |
| `ship retry <run>`, `ship goto <run> <step>`, `ship cancel <run>` | Step in manually |
| `ship budget <run> --usd 5 --tokens 1m [--retry]` | Raise a run's budget, and retry a run that stopped at it |
| `ship usage` | Claude's usage limits, and what each run spent this window |
| `ship focus <run>` | Pause every other active run so this one can finish |
| `ship upgrade <run> [--step s]` | Move a run onto its pipeline as you've since edited it |
| `ship clean [--run r]` | Remove the worktrees of finished runs |
| `ship prune [--older-than 30d] [--dry-run]` | Delete the records of runs that finished long ago |
| `ship cd <run>` | Print the worktree path: `cd "$(ship cd 3fa)"` |
| `ship open [run]` | Open the web UI |
| `ship validate` | Check this repo's pipelines |
| `ship feedback <run> [--step s] "…"` | Record feedback on a run's work |
| `ship pipeline refine <pipeline>` | Propose a new version from the feedback |
| `ship pipeline stats <pipeline>` | Time, cost, tokens and how hands-on each step was, across recent runs |
| `ship pipeline versions <pipeline>` | A pipeline's versions and what changed in each |
| `ship pipeline diff <pipeline> v1 v2` / `restore <pipeline> v1` | Compare versions, or go back to one |
| `ship pipeline migrate <pipeline>` | Turn a single-file pipeline into a folder |
| `ship pipeline default [pipeline]` | Show or set the pipeline `ship start` uses when neither it nor the brief names one (`--global` for every repo, `--clear` to remove) |
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

**Fix the pipeline mid-run.** A run works from its own copy of the pipeline,
taken when it started, so editing the file doesn't change runs in flight.
When a run hits a problem in the pipeline itself, fix the file, then
`ship upgrade <run> --step <step>` (or **Upgrade run** on the run's page,
which appears once the file has changed) moves the run onto the new version
and continues at the step you pick, by default the one it's on (or, at a
check-in, the step that led there). A running step is stopped, as with goto,
and the run counts as the new version from then on. Skills, rules and scripts
still come from the run's worktree: commit fixes to those on the run's branch
(ship warns about any it can see are out of date there).

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

**Clearing out old runs.** ship keeps everything about a run (its event log,
agent transcripts, handovers) in `~/.ship/state/runs/`, and transcripts add
up. `ship prune` deletes the records of runs that finished more than 30 days
ago (`--older-than 7d` to change it, or `retention.keep_runs_days` in the
config; 0 there means it prunes nothing unless you pass `--older-than`). A run's slices go with it. Runs still going are never touched, and
runs that still have a worktree are kept, so you don't lose work, unless you
pass `--force`, which removes the worktree too. `--dry-run` lists what would
go and how much space it frees. Pipeline history and feedback live in the
repo and are kept. The runs page's **Finished** filter has the same thing as
a button.

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
- **Repo skills (including a pipeline folder's), rules and scripts come
  from the run's branch.** Agents and
  scripts run in the worktree, so edits in your checkout reach runs started
  from a base that includes them.
- **Your user-level skills (`~/.claude/skills`) are read at every step.**
  Editing one changes the next agent step of every run in progress.

## Improving pipelines with feedback

You judge the work; `ship` keeps track of what you said and which version of
the pipeline it was about, and Claude helps you act on it.

**Versions are automatic.** A pipeline's version is a fingerprint of the
pipeline file, any pipelines it fans out to, every skill or slash command its
steps call (found in the pipeline's folder, the repo's `.claude/skills/` or
your `~/.claude/skills/`, including all the skill's files), and the rules
files and helper scripts it uses. Each run records the version it used,
computed from exactly what its agents see: its own copy of the pipeline and
its worktree's skills, rules and scripts. When anything in that set changes,
whether you edited it or `refine` did, the next run is the next version (v1,
v2, …).

Each version keeps a copy of every one of those files as they were (each
distinct file stored once, however many versions use it), so you can see
exactly what changed and go back:

```sh
ship pipeline versions pr          # every version: when, why, what changed, feedback
ship pipeline diff pr v3 v4        # what changed between two versions
ship pipeline diff pr v3           # …or between a version and the files now
ship pipeline restore pr v3        # put the pipeline and its skills back as in v3
```

`restore` writes each file back to where it lives now (or where it lived
then, if it's since been removed), and tells you what it changed. It won't
overwrite files with uncommitted changes, or files outside the repo such as
your own `~/.claude/skills` (or what a symlink in a skill points at), unless
you pass `--force`; symlinks are followed, never removed. Review the changes
and commit them; the next run is v3 again.

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

### Stats, and whether a new version is better

Every run that finishes adds a record to its pipeline's history: its
version, how it ended, time, cost and tokens, and for each step the visits,
outcomes, time, cost, tokens and errors. It also records **how hands-on the
run was**, because the less a person has to step in, the better the pipeline
is doing:

- **Check-ins:** questions you answered (asks, split reviews, variable
  prompts), and the choices you made.
- **Interventions:** retries, gotos, upgrades, raised budgets, resumed
  sessions, set variables, cancels, each against the step the run was on.
  Pausing and resuming are counted but treated as scheduling, not help.
- **Waiting for a person:** how long the run sat waiting for you.
- **Notes** you wrote at check-ins, for the next step or the whole run.
- **Fully autonomous:** the run had no check-ins and no interventions
  before it reached its PR (or at all, if it has no `pr:` step). Pausing and
  resuming don't count. What happens on the PR doesn't either: that's the
  next measure.
- **PR rounds:** review comment batches and CI failures sent to a fixer,
  until the PR was ready or merged; and feedback recorded against the run.

`ship pipeline stats pr` averages the recent runs per step (time, cost,
tokens, and how often each step needed a person) under a headline of how
hands-on the runs were. It only reads the records.

**The pipeline page** in the web UI (click a pipeline's name on the
Pipelines page) shows it all, version by version:

- **Overview:** runs, success rate, typical time and cost, hands-on numbers
  and histograms, and a table of the same per version.
- **Steps:** visits, pass rate, time (with a small histogram of its spread),
  cost, tokens, how often it needed a person, and suggestions. A step
  *passes* when its outcome leads nearer to done and *fails* when it sends
  work back, stops the run or errors. A deciding step that passes 95% of
  the time over 10 or more visits may not be needed; one that fails more
  than 60% of the time is often let down by the step before it; one that
  takes half of the time or cost may be doing too much; a tiny agent step
  may be worth merging into a neighbour.
- **Versions:** what changed in each, run and feedback counts, and the
  restore and diff commands.
- **Compare versions** (the latest against the one before, or any two):
  success rate, time, cost, tokens, hands-on, waiting, fully autonomous
  and PR rounds, then each step's pass rate, time and need for a person.
  For each it shows the distributions, and how likely the newer version is
  better given the runs so far: **likely better** at 95% or more,
  **probably better** at 80% or more, the same for worse, otherwise **no
  clear difference**, and **too few runs** under 3 runs per version
  (`stats.min_runs`). Rates use a Beta-Binomial model, counts per run a
  Gamma-Poisson model, and time, cost and tokens are compared as ratios on
  a log scale, each with a 95% range for the change. Time and cost count
  runs that finished done; a link on the page includes stopped and failed
  runs too.
- **Feedback** on the pipeline, with what's open and what's been addressed.

Dry runs (`--fake-agents`) aren't recorded, so they don't skew the numbers
(`stats.include_fake_runs: true` records them). `stats.record_runs: false`
stops recording altogether. Viewing the page and running `ship pipeline
stats` never write anything. Runs that finished on your machine without a
record (from before ship kept them, say) are added from `~/.ship/state` only
when you ask: with `ship pipeline stats <pipeline> --backfill`, or the
**Backfill from N past runs** button the page shows when there are some.

### Where the history lives

A pipeline folder holds its own history, beside `pipeline.yml`; a
single-file pipeline's lives in `.ship/history/<pipeline>/` (global ones in
`~/.ship/history/`). Commit it with the pipeline, so the reasoning behind
each version, and everyone's runs and feedback, travel with it:

```
versions/<hash>/version.json     what the version is made of: each part and its content hash
versions/<hash>/seen/<id>.json   when it was first seen, by whom, and why
objects/<sha256>                 the content of every file a version used, for diff and restore
runs/<run-id>.json               one finished run
feedback/<id>.json               one piece of feedback (and .closed-… when dealt with)
proposals/, reports/             from refine and report
```

Every record is its own file, written once and never changed, so several
people using the same pipeline on different machines can commit their
history without conflicts. `objects/` is content-addressed: each file is
stored once, named by the hash of its content, however many versions use it
(a skill folder's listing of its files is stored the same way). So a new
version adds only the files that changed, and two machines saving the same
file write the same object. `ship pipeline diff` and `ship pipeline restore`
read the copies from there. Version numbers (v1, v2…) and feedback numbers
(#1, #2…) are worked out from the dates when read.

History from older versions of ship (`versions.jsonl`, `feedback.jsonl`) is
read as it is, and written out as record files the next time ship records
something in that history (a run starting, feedback, a refine). The old
files are left in place; once converted, you can delete them.

## Briefs

A run starts from a brief: markdown with a little YAML at the top. The
`/ship` skill writes these for you; you can also write one yourself.

```markdown
---
title: Rate limit the public API          # required
pipeline: pr                              # which pipeline (optional with a default, or only one)
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
read and edit. A pipeline is either a folder, `.ship/pipelines/<name>/` with
a `pipeline.yml` (what `/ship-design` writes), or a single file,
`.ship/pipelines/<name>.yml`; the folder or file name is the pipeline's name.
`ship init` sets up VS Code (`.vscode/settings.json`) and JetBrains IDEs
(`.idea/jsonSchemas.xml`) to autocomplete and check both against the schema.

### Pipeline folders and their own skills

```
.ship/pipelines/pr/
  pipeline.yml
  skills/review/SKILL.md          # the pipeline's own skills
  versions/ objects/ runs/ …      # its history (see above)
```

A folder keeps everything about a pipeline together: the pipeline, the
skills written for it, and its history. Its skills reach every agent of the
pipeline as a Claude Code plugin named after it: before each agent step,
ship assembles the plugin in the run's own dir (a manifest, and the
folder's `skills/` as the run's worktree has them) and passes it with
`--plugin-dir`, so nothing is written into your checkout. A step calls a
folder skill as `agent: /review`, or `/pr:review` to be explicit (do that
if a repo or user skill has the same name). Like repo skills, folder skills
come from the run's branch, so commit them. `ship validate` warns (W108)
when a step calls a skill it can't find in the folder, the repo's
`.claude/skills/` or your `~/.claude/skills/`.

To turn a single-file pipeline into a folder:

```sh
ship pipeline migrate pr            # moves pr.yml and its history into .ship/pipelines/pr/
ship pipeline migrate pr --skills   # also moves the repo skills only pr uses
```

Files git tracks are moved with `git mv`, so the moves are staged; commit
them.

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
| `ask:` | Pauses for a person. Uses `choices:` instead of `next:`. `input: none \| optional \| required` controls the note box. | The label of the button pressed. The note becomes the handover; a note marked for the whole run also reaches every later agent step. |
| `wait:` | Polls a command every `every` (default `1m`) until the last line it prints matches a key of `next`. `timeout` defaults to `24h`. | The keys of `next`, plus `timeout` if you map it. |
| `pr:` | Watches a pull request, no agent involved (see [Watching pull requests](#watching-pull-requests)). | `feedback`, `ci_failed`, `ready`, `merged`, `closed`, `timeout`. |
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

### Budgets and usage limits

Give a run a budget in dollars, tokens or both, and agent and split steps a
budget per visit:

```yaml
limits:
  max_budget_usd: 20     # the whole run (slices have their own pipeline's limits)
  max_tokens: 5m         # 200k, 1.5m and plain numbers all work
steps:
  review:
    agent: /review
    max_budget_usd: 2    # each visit to this step
    max_tokens: 400k
```

- **A step's budget** works like an error: the agent is stopped, and the
  step's `error` outcome goes to `on_error`, or the run waits in your inbox.
- **The run's budget** always holds the run in your inbox ("budget reached
  at …"), since retrying can't help until someone decides to spend more.
  Raise it from the run's page, or with `ship budget <run> --usd 5 --retry`
  (`--tokens 1m` for a token budget). The run's page shows what it has
  spent against the budget, raised amounts included.
- **Stopping at a token budget** means stopping claude before it reports
  what the invocation cost, so ship estimates it from what the run's agents
  have cost per token so far, and the visit's error says it's an estimate.
- **Claude's usage limits.** When an agent hits your account's 5-hour or
  weekly limit, the step waits until the limit resets (the run shows as
  waiting) and then carries on in the same conversation, so nothing is
  lost. Waits follow the clock, so if your laptop sleeps through one it
  carries on within half a minute of waking once the limit has reset (the
  same goes for `wait:` steps' polls and timeouts). `ship usage` and the meter in the web UI's top bar show how much
  of each limit is used and what each run spent in the current window. If
  several runs are going and the window is nearly used up, `ship focus
  <run>` pauses the others after their current step so that one can finish;
  `ship resume <run>` picks them up later.

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

### Watching pull requests

A pipeline that opens a PR should keep watching it until it's merged. A `pr:`
step does that without an agent, using the `gh` CLI:

```yaml
  in-review:
    pr: ""             # the PR for the run's branch (or a PR number or URL)
    every: 2m          # how often to look
    settle: 10m        # see below
    trigger: auto      # or manual
    max_visits: 0      # it's re-entered after every fix, so don't cap it
    next:
      feedback: address-review   # new review comments
      ci_failed: fix-ci          # a check failed on the latest commit
      merged: done
      closed: stop
```

- **Review comments are batched.** Reviewers comment over minutes or hours, so
  the step collects new conversation comments, reviews and inline comments,
  and sends them as one batch once nobody has commented for `settle`. With
  `trigger: manual` it waits for you instead: the run's page shows
  **Address N now**, and `ship pr <run> --address` does the same
  from the terminal (both also work in auto mode, to skip the wait).
  Comments starting with `ship:` are feedback on the pipeline, not
  instructions, so they're left out; so are replies the agents post (which
  include `<!-- ship-agent -->`) and bots' conversation comments (coverage
  reports, preview links). Bots' reviews and inline comments, such as an AI
  reviewer's, still count.
- **CI is judged when it's finished.** `ci_failed` fires once every check on
  the PR's latest commit has finished and one failed, once per commit. A check
  that was cancelled is re-run once first (when its Actions run has finished),
  unless another job in the same run failed: then that failure is the
  verdict and is reported straight away. No checks reported yet doesn't count
  as passing.
- **The details go in the handover.** The fixing step receives each new
  comment (author, file and line, link) or each failing check with the end of
  its failed log.
- `ready` fires when the PR is approved (on repos that don't require
  reviews: someone approved and nobody's latest review requests changes)
  and every check passed; outcomes you
  don't map just keep it watching. `ship pr <run>` shows what it sees.

### Variables

```yaml
variables:
  ticket: { from_brief: true, format: '^[A-Z]+-[0-9]+$' }  # from the brief's vars: (or --var)
  test:   { value: make test }                             # fixed
  pr_url: { set_by: open-pr }                              # saved by a step
  owner:  { ask: "Who should review this?" }               # asked the first time it's used
  epic:   { from_parent: ticket }                          # in a slice pipeline: the parent run's value
```

**Slices and tickets.** A slice pipeline's `from_brief` and `from_parent`
variables default to the parent run's values, but each slice can set its
own. When a brief covers several tickets, the split agent gives each slice
the ticket it delivers (it's told which variables the slice pipeline takes),
so `branch: "feat/{{vars.ticket}}"` gives every slice its own branch. The
split review shows each slice's values, and you can change them under
`vars:` in the slice files before approving.

A slice pipeline also runs on its own: `ship start child --var ticket=API-9`.
Its `from_parent` variables then come from `--var` or the brief, and
`{{slice.*}}` and `{{parent.*}}` are empty.

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
  allowed_tools: ["Bash"]     # the default; a list here replaces it

workspace:
  branch: "feat/{{vars.ticket}}"   # default: ship/<run-id>
  base: main                       # default: the repo's default branch
  provider: auto                   # auto | git | treehouse | none (see Worktrees)
```

Agents run unattended, in the worktree, with your project's
`.claude/settings.json`, skills and `CLAUDE.md`. They run lean: only the
core tools (Bash, Read, Edit, Write, Glob, Grep, plus Skill for steps that
use skills) and no MCP servers, because every turn re-reads everything the
agent was given. Add tools with `tools: [WebFetch]`, MCP servers with
`mcp_config: [path/to/mcp.json]`, or give a step everything your Claude
Code has with `lean: false`.

A step that continues a conversation (`session:`) starts a fresh one
instead, reading the last handover, when the conversation has been idle
for over an hour (the prompt cache has expired, so resuming would re-send
all of it) or has grown past about 150k tokens. `ship status` shows each
visit's cache use and flags resumes that look cold. Tools they're refused are
flagged in the UI. Bash is allowed by default; to narrow it, list what
they may use in `allowed_tools` (such as `"Bash(make *)"`), which replaces
the default.

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

Templates are built into `ship` or your own, in `~/.ship/templates/<name>/`.
Yours win if the names clash. Built in so far:

- **`rigorous`**: implement, then an adversarial review that's fresh every
  round (findings need a concrete failing sequence, name every affected site,
  and include a simplification pass), fixes in a separate session, live
  verification with evidence, checks, a PR written for a reviewer who wasn't
  there, then a native PR watch that sends review comments and CI failures to
  a fixer until it merges. Thorough and token-hungry; adapted from
  [no-mistakes](https://github.com/kunchenguid/no-mistakes).

A template is a folder:

```
<name>/
  template.yml          # description: one line shown by `ship templates`
  pipelines/<p>/        # a pipeline folder, with its skills → .ship/pipelines/<p>/
  pipelines/*.yml       # single-file pipelines → .ship/pipelines/
  bin/*                 # → .ship/bin/ (made executable)
  rules/*               # → .ship/rules/
  skills/<skill>/       # shared skills → .claude/skills/<skill>/
```

`rigorous` installs as a folder, `.ship/pipelines/rigorous/`, with its
skills inside.

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
| `<repo>/.ship/pipelines/<pipeline>/` | a pipeline folder: `pipeline.yml`, its skills, and its history (versions, run stats, feedback, proposals, reports; commit all of it) |
| `<repo>/.ship/history/<pipeline>/` | a single-file pipeline's history (commit it) |
| `<repo>/.claude/skills/ship-*` | the Claude Code skills |
| `~/.ship/state/runs/<id>/` | everything about a run: its event log, brief, and each step's input, output and handover (`ship prune` clears out old ones) |
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
