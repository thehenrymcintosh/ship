---
name: ship
description: Hand a planned piece of work over to a ship pipeline. Use when the user says to ship something ("ship this", "ship it", "ship a fix for…", "ship the plan"), or to kick off, hand over, or send the plan to the pipeline.
---

# Hand over to ship

1. Run `ship ls --pipelines` to see available pipelines. If the user named one, use it.
   Otherwise, if one is marked (default), use it without asking. Otherwise, if more than one
   could fit, ask which one. If there are none, stop and tell the user to create one first:
   either run `/ship-design` (it designs a pipeline with them in plain language) or pick a
   template with `ship templates` and `ship add <template>`.
2. Write a brief from this conversation in exactly this format:

   ---
   title: <short imperative title>
   pipeline: <pipeline>
   vars:
     <any variables the pipeline needs, e.g. ticket>
   acceptance:
     - "<testable criterion>"
   ---

   ## Context
   <why, background, constraints, links>

   ## Plan
   <the agreed approach, key decisions, files/modules involved>

   ## Notes for splitting
   <how the user wants this broken into PRs, if discussed>

   Acceptance criteria are required. If none were agreed, propose some and confirm with the user.
   The front matter is YAML, so a `: ` or ` #` inside a value breaks it: keep each acceptance
   item in double quotes (escaping any `"` inside as `\"`), and quote the title or a var too
   when it contains one.
3. If the user wants this to wait for another run ("stack it on <run or description>",
   "start it after the current release"), find that run: `ship ls` lists the active runs
   (`--all` includes finished ones) with their id, pipeline and title. Match it by id, title
   or pipeline; if more than one could fit, or none does, ask which. Pass `--after <run id>`
   below: the new run waits, without a worktree or agent, until that run is done, then starts
   from its base as it is then (after a release has merged, that includes the release). Add
   `--stack` only when the user wants it built on that run's branch rather than the base,
   e.g. "on top of its branch" or when that run's work won't be merged first.
4. Start the run, passing the brief on stdin (with `--after <run id>` from step 3, if any):

   ship start <pipeline> --brief - --no-open <<'BRIEF'
   <the brief>
   BRIEF

5. Report the run id and URL from the output (and, with `--after`, that it starts once the
   other run is done; `ship start-now <run>` starts it sooner). Do not start implementing the
   work yourself.
