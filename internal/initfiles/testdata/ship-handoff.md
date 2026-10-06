---
name: ship-handoff
description: Hand a planned piece of work over to a ship pipeline. Use when the user says to kick off, hand over, ship, or send the plan to the pipeline.
---

# Hand over to ship

1. Run `ship ls --pipelines` to see available pipelines. If more than one could fit and the user
   didn't say, ask which one. If there are none, stop and tell the user to create one first:
   either run `/ship-design` (it designs a pipeline with them in plain language) or pick a
   template with `ship templates` and `ship add <template>`.
2. Write a brief from this conversation in exactly this format:

   ---
   title: <short imperative title>
   pipeline: <pipeline>
   vars:
     <any variables the pipeline needs, e.g. ticket>
   acceptance:
     - <testable criterion>
   ---

   ## Context
   <why, background, constraints, links>

   ## Plan
   <the agreed approach, key decisions, files/modules involved>

   ## Notes for splitting
   <how the user wants this broken into PRs, if discussed>

   Acceptance criteria are required. If none were agreed, propose some and confirm with the user.
3. Start the run, passing the brief on stdin:

   ship start <pipeline> --brief - --no-open <<'BRIEF'
   <the brief>
   BRIEF

4. Report the run id and URL from the output. Do not start implementing the work yourself.
