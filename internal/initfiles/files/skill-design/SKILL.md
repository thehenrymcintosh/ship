---
name: ship-design
description: Design, explain or change ship pipelines in plain language. Run it as /ship-design followed by what you want the pipeline to do.
disable-model-invocation: true
argument-hint: "[what the pipeline should do, or which pipeline to change]"
---

# Design a ship pipeline

The user wants help designing a ship pipeline. Their request: $ARGUMENTS

They may not know the YAML format, and they shouldn't need to. Talk about
the workflow in plain language, then write and check the file yourself.

## How to work

1. **Look first.** Run `ship ls --pipelines` to see the existing pipelines and
   where each comes from (repo or global). Read any that are relevant. If the
   user wants to change one, start from it. Also run `ship templates`: if a
   template is close to what they want, suggest `ship add <template>` and
   adapt the result rather than starting from scratch.
2. **Understand the workflow.** Ask short questions, a few at a time, only
   about what you can't infer:
   - What starts the work, and what does "done" look like?
   - Which steps should an agent do, and which are scripts (tests, lint,
     push, opening a PR)?
   - Where should a person step in: always, or only when something goes
     wrong or an agent is stuck?
   - Should big work be split into several PRs, built one after another
     (stacked) or side by side?
   - Does anything need waiting for (CI, PR review, a merge)?
   - Should it live in this repo (`.ship/pipelines/`) or be shared by every
     repo (`~/.ship/pipelines/`)?
3. **Describe it before writing it.** Give a numbered plain-language outline:
   each step, who does it, and where each result leads. For example: "3.
   Run the tests. If they pass, open the PR. If they fail, go back to step 1
   with the failures." Get a yes or corrections.
4. **Write the YAML**, starting with the schema comment line (below).
5. **Check it.** Run `ship validate <file>` and fix every error. Explain any
   warning that remains in one sentence; some are worth keeping. Then run
   `ship graph <name>` and walk the user through the flow in words.
6. **Offer to keep it as a template** if it's something they'd reuse in other
   repos: copy it to `~/.ship/templates/<name>/` with a `template.yml`
   (`description: …`), the pipeline under `pipelines/`, and any helper scripts
   (`bin/`), rules (`rules/`) and agent skills it calls (`skills/<skill>/`).
   It then shows up in `ship templates`.
7. **Offer a dry run** when it would help:
   `ship start <name> --brief brief.md --fake-agents script.yml --no-open`,
   with a small script of agent outcomes (format below), so they can watch
   the flow without spending on real agents.

Keep the explanation in the user's terms ("the reviewer agent can send it
back to be fixed") rather than YAML terms ("`changes: implement`"), unless
they ask.

## Pipeline file reference

```yaml
# yaml-language-server: $schema=../schema/pipeline.json
version: 1                    # required, always 1
description: One line shown in the UI
start: implement              # first step (required)

workspace:                    # optional
  provider: git               # git (default, a worktree per run) | treehouse | none (main checkout)
  branch: "feat/{{vars.ticket}}"  # default ship/<run-id>
  base: main                  # default: the repo's default branch

agent:                        # defaults for every agent step (all optional)
  model: sonnet               # sonnet | opus | haiku | full model id
  effort: medium              # low | medium | high | xhigh | max
  permission_mode: acceptEdits   # default | acceptEdits | plan | bypassPermissions
  allowed_tools: ["Bash(make *)"]

variables:                    # each has exactly one source
  ticket: { from_brief: true, prompt: "Ticket?", format: '^[A-Z]+-[0-9]+$' }
  test:   { value: make test }
  pr_url: { set_by: open-pr }     # saved by that step
  owner:  { ask: "Who owns this?" }   # asked the first time a step uses it

defaults:
  max_visits: 5               # per step per run; 0 = unlimited
  when_exhausted: check-in    # where to go when a step hits max_visits (else the run pauses)
  on_error: check-in          # where `error` outcomes go (else the run pauses)
  timeout: 30m                # per visit (90s, 5m, 2h, 7d)

limits: { max_transitions: 200, max_budget_usd: 10 }

steps:                        # names: lowercase letters, digits, - and _
  implement:
    agent: /implement         # a skill line; or prompt: "free text"; or both
    session: continue         # revisits resume the same agent session
    next: review              # one target for every outcome

  review:
    agent: /review mode=code
    model: opus
    next:                     # the agent chooses one of these outcomes
      pass: gate
      changes: implement
      stuck: check-in

  gate:
    description: Clean tree and green tests
    run: |                    # bash; exit 0 → pass, anything else → fail
      test -z "$(git status --porcelain)" || { echo "uncommitted changes"; exit 1; }
      {{raw vars.test}}
    next: { pass: open-pr, fail: implement }

  open-pr:
    run: gh pr create --fill --head {{run.branch}} --base {{run.base}}
    save: { pr_url: last_line }   # last_line | stdout | file:<path> | json:<dotted.path>
    next: { pass: in-review, fail: check-in }

  in-review:
    wait: .ship/bin/pr-status {{vars.pr_url}}   # polled; its last output line picks the outcome
    every: 5m
    timeout: 14d
    next: { merged: done, comments: implement }

  check-in:
    ask: "{{brief.title}} stopped at {{came_from}}. What next?"
    show: [prev.handover]     # prev.handover | brief | vars | slices
    input: optional           # none | optional | required note
    choices:                  # button label → where it goes
      retry: $came_from
      rework: implement
      abandon: stop
```

**Step types** (exactly one per step):

| Key | What it does | Outcomes |
|---|---|---|
| `agent:` / `prompt:` | Runs Claude Code in the run's worktree. | The keys of `next` (the agent picks one), or `done` if `next` is a single target |
| `run:` | Runs a bash script. | `pass` / `fail`, or map exit codes with `outcomes: {0: pass, 2: flaky, default: fail}` |
| `ask:` | Pauses for a person. Uses `choices:` instead of `next:`. | The chosen label |
| `wait:` | Polls a command every `every` (default 1m) until its last line matches a key of `next`. | Those keys; `timeout` if mapped |
| `split:` | An agent splits the brief into slices (`rules:` file, `max_slices:`, `review: true` to approve them). | `ok` (required) plus any others you add |
| `fanout:` | Runs another pipeline once per slice. `mode: series` (`stack: true` bases each slice's branch on the previous one; `advance_on: <step>` starts the next slice when this one reaches that step) or `mode: parallel` (`max_parallel: 3`). `on_child_stop: halt \| continue`. | `done` (every slice finished) and `failed`, both required |

**Targets**: a step name, `done` (success), `stop` (abandon), or `$came_from`
(back to the step that led here, for "retry" buttons). Every step also has an
implicit `error` outcome (crash, timeout, invalid agent result, unset
variable). Route it with `on_error:` or `next: {error: …}`.

**Placeholders** in prompts, scripts, questions and `workspace.branch`:
`{{vars.NAME}}`, `{{brief.title}}`, `{{brief.path}}`, `{{brief.acceptance}}`,
`{{run.id}}`, `{{run.branch}}`, `{{run.base}}`, `{{run.worktree}}`,
`{{run.repo}}`, `{{prev.step}}`, `{{prev.outcome}}`, `{{prev.summary}}`,
`{{came_from}}`, `{{visit.number}}`, and in slice pipelines `{{slice.key}}`,
`{{slice.number}}`, `{{slice.title}}`, `{{parent.vars.NAME}}`. In `run:` and
`wait:` scripts each value is shell-quoted as one word; write
`{{raw vars.test}}` to run a variable as a command. Steps also get env vars
such as `SHIP_RUN_ID`, `SHIP_RUN_DIR`, `SHIP_BRANCH`, `SHIP_VAR_<NAME>` and
`SHIP_HOME`.

## Design rules of thumb

- Give every agent a way to reach a person: an outcome such as `stuck`
  leading to an `ask` step, or `on_error:`. Otherwise failures just pause the
  run.
- Use `max_visits` plus `when_exhausted` so fix-and-review loops can't spin
  forever. A person's choice resets the counter.
- Put deterministic checks (tests, lint, clean tree) in `run:` steps rather
  than asking an agent whether they pass.
- Name agent outcomes for what happens next (`changes`, `stuck`, `pass`). The
  agent sees each outcome alongside its target's description, so give steps a
  `description:`.
- Put helper scripts in `.ship/bin/` (repo pipelines) or `~/.ship/bin/`
  called as `"$SHIP_HOME/bin/…"` (global pipelines), and make them
  executable.
- `fanout` must come after a `split` step, and the pipeline it names must
  exist next to this one (or globally).
- Agent steps run unattended. If a step needs a tool the project doesn't
  allow, add it to `allowed_tools`; denied tools show up as warnings in the UI.

## Fake-agent script for dry runs

Visit N of a step uses entry N; the last entry repeats.

```yaml
implement: [{outcome: done, summary: "wrote the code"}]
review:
  - {outcome: changes, summary: "missing tests"}
  - {outcome: pass, summary: "looks good"}
split:
  - outcome: ok
    summary: two slices
    slices:
      - {key: schema, title: Add tables, brief: "…", acceptance: ["migration runs"]}
      - {key: api, title: Enforce limits, brief: "…", acceptance: ["429 returned"]}
```
