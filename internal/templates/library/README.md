# Built-in pipeline templates

Each subdirectory is a template that `ship add <name>` copies into a repo
(or into `~/.ship` with `--global`). Templates get added here once they've
proven useful in practice. Your own templates can live in
`~/.ship/templates/<name>/` with the same layout; they're listed alongside
these and win on a name clash.

```
<name>/
  template.yml                     # description: one line shown by `ship templates`
  pipelines/<pipeline>/pipeline.yml   # a pipeline folder → .ship/pipelines/<pipeline>/
  pipelines/<pipeline>/skills/<skill>/  # its own skills, loaded as a plugin
  pipelines/*.yml                  # or single-file pipelines → .ship/pipelines/
  bin/*                            # → .ship/bin/ (made executable)
  rules/*                          # → .ship/rules/
  skills/<skill>/...               # → .claude/skills/<skill>/ (skills shared with the user's own)
```

Prefer pipeline folders, with the skills a pipeline's agent steps use inside
the folder: they travel with the pipeline, are versioned with it, and don't
clash with a user's own skills. Skills under the template's top-level
`skills/` are installed into `.claude/skills/`; name those with a `ship-`
prefix (e.g. `skills/ship-review/SKILL.md`, used as `agent: /ship-review`).
