# Feature walkthroughs

Everything below is recorded against a fully synthetic environment
(`scripts/demo/setup.sh`); regenerate any recording with
`bash scripts/demo/setup.sh && vhs scripts/demo/<name>.tape`.

## Search: filter, transcript hits, AI find

<img src="../assets/demo-search.gif" alt="Filtering, transcript hit-count search, and AI find" width="900">

Three tiers, one flow:

- `/` filters whatever pane you're looking at as you type — folder
  names, session titles, custom names, ids.
- `ctrl+s` escalates the same query to a full-transcript search: every
  session under the current root, ranked by hit count (the session that
  mentions a term thirteen times is the one you meant), with the best
  matching line previewed for the selection. `enter` opens the session.
- `ctrl+g` opens AI find: describe the session you half-remember in
  plain words, and the model of your choice (claude, codex, or fully
  local via ollama — auto-detected, never a hosted service of ours)
  ranks likely matches with a short reason each. Fired searches are
  saved and replay instantly, and hit counts show beside the ranking.

## Rescue: lost sessions, restore, resume

<img src="../assets/demo-rescue.gif" alt="Revealing lost sessions, restoring from backup, resuming" width="900">

- `x` reveals sessions that exist only in the agent's prompt history —
  deleted before any backup could catch them. They are permanently
  marked `✕ lost`; nothing disappears silently.
- A `✝ recover` session (deleted from disk, but backed up) restores
  with `r`: the dialog previews exactly which file goes where, and the
  restored session gets a `✚ restored` badge until its next live write.
- `o` opens any session: a running one gets its terminal window raised;
  a closed one resumes in a fresh terminal window or right in the
  current one — your choice, in the right project directory. On a lost
  session that has been rebuilt, `o` routes straight to the rebuild.

## Transplant: move sessions between projects

<img src="../assets/demo-transplant.gif" alt="Transplanting a session to another directory" width="900">

`t` relocates a session (or a whole project's sessions, from a folder
row) to any directory — including one that doesn't exist yet. The plan
previews everything before anything happens: which sessions move, the
directory that will be created, and what happens to project memory.
`tab` switches move to copy — copies mint a fresh session identity so
no id ever exists in two places. Moves are copy-first: the source is
committed to backup before anything moves, and originals are removed
only after verification.

The same operation is scriptable:

```sh
sp transplant --project ~/old/place --to ~/new/place --dry-run
```
