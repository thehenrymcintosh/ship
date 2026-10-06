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
3. Start the run, passing the brief on stdin:

   ship start <pipeline> --brief - --no-open <<'BRIEF'
   <the brief>
   BRIEF

4. Report the run id and URL from the output. Do not start implementing the work yourself.
