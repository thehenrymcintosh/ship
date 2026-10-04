# How we split work into PRs

- Each slice is one PR that can be reviewed in under ~20 minutes (aim for < 400 changed lines).
- Order slices so each one builds on the previous and leaves `main` releasable:
  schema/migrations → internal APIs → behaviour behind a flag → UI → remove flag.
- Every slice has its own tests and acceptance criteria.
- No slice may leave the build red or contain dead code that a later slice "will use"
  unless it is behind a flag.
- If the work fits in one PR, produce a single slice.
