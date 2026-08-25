# Recovery: restore, rescue, and what each state means

SessionProtect distinguishes several recovery-related states that sound
similar but are mechanically very different.

| State | Meaning | What still exists | Remedy |
| --- | --- | --- | --- |
| `✝ recover` | Transcript deleted from the agent's storage, **but present in backup** | The full transcript, in the backup repo | `r` → **Restore** |
| `✕ lost` | Transcript gone from disk **and** backup — deleted before protection existed | Only your prompts, in the agent's own history file | `r` → **Rescue** |
| `✕ rescued` | A lost session that has at least one living reconstruction | The prompts, plus one or more rebuilt sessions | `o` resumes the rebuild |
| `✚ restored` | A recover session brought back | The full transcript, live again | — |
| `⟳ rebuilt` | A **new** session reconstructed from a lost one's prompts | A synthesized transcript | — |

**Restore is perfect recovery; rescue is partial recovery.** Sessions
pruned before SessionProtect existed left nothing behind except the
agent's `history.jsonl`, which records every prompt you ever typed
(text, session id, project, timestamp) — but none of the responses.
Rescue works entirely from that surviving half.

## How lost sessions are detected

Every scan reads the agent's history file (always read-only) and asks:
which session ids appear here but exist neither live nor in backup?
Those surface as `✕ lost` — hidden behind the `x` toggle so they don't
crowd living sessions, but always findable through search (an active
query deliberately overrides the toggle: a lost session is the one you
most need to find). Codex history records no project path, so codex
lost sessions pool under a single pseudo-folder.

"Lost" is a permanent designation. SessionProtect never rewrites agent
history to erase the record of a loss.

## Restore (`✝ recover` → `✚ restored`)

`r` on a recover session previews exactly which file goes where, then
copies it back byte-for-byte through the restore engine: concurrency
lock, timestamped safety copy on any overwrite, audit entry. The
restored file gets a **fresh mtime** on purpose — the agent's retention
cleanup deletes by file age, so restoring with the original timestamp
would mark the session for immediate re-deletion.

## The rescue dialog

`r` on a lost session opens a two-page dialog. Page one offers:

- **Resume** — shown only when reconstructions already exist, and then
  as the pre-selected default: enter goes straight to resuming the
  existing rebuild (or a chooser when there are several) instead of
  creating another.
- **Rebuild…** — advances to page two: **Rebuild** (mechanical) or
  **Rebuild with AI**, with Back as the safe default.
- **Export prompts** — the tier-1 artifact.
- **Cancel** — always available; the description under the buttons
  follows the highlight, so each option explains itself.

Every action then asks **where to write** through a directory picker:
arrow through subfolders, `←`/`..` to go up, `/` filters the list
(same key as the main browser), `~` jumps to a typed path, and
**+ new folder…** names a directory that is created only on confirm.
Rebuilds default to the session's original project directory —
recreated if it no longer exists, so the resume always has a real
working directory to land in.

## Rescue tier 1 — Export prompts

**Export prompts** writes a markdown artifact — by default into the
session's project directory (falling back to
`<backup root>/exports/`): project, prompt count and time span, then
every surviving prompt with its timestamp — plus an honest header
noting that only the human side survives. Works for both agents,
re-runnable any time.

## Rescue tier 2 — Rebuild (claude)

**Rebuild** synthesizes a **new, genuinely resumable session**:

- Your prompts become user messages, each followed by an assistant
  placeholder `[response lost]`; the final placeholder tells the
  resumed agent its situation — that the original transcript was lost
  and the conversation continues from the surviving prompt history.
- The file is structurally a real transcript (message chain, real
  timestamps from history) written under a **fresh session id**,
  created exclusively so it can never overwrite anything. Assistant
  messages carry `model: "reconstructed"` — honest provenance that
  claude's interactive resume requires to load the transcript; on
  resume the agent falls back to your configured default model and
  says so.
- The rebuild is synced to backup immediately, so it is protected from
  the moment it exists.
- Responses are *not* recovered (they are gone) and *not* invented —
  the placeholders are honest.

When a rebuild completes, a dialog offers to resume it immediately —
in a new window or in place — or **Later**; either way the list now
shows the pair: the rebuild as `⟳ rebuilt`, the original as
`✕ rescued`.

## Rescue tier 3 — Rebuild with AI (claude)

**Rebuild with AI** goes one step further: a model of your choice
reads the full prompt sequence and writes a *reconstruction brief* —
the session's inferred goal, the arc of the work, decisions the
prompts imply, and the open threads — which becomes the final message
of the new session.

The model picker lists every backend found on your machine, enumerated
live so it can never go stale: **claude** CLI aliases (opus first for
synthesis), every **codex** model reported by the codex CLI itself,
and every installed **ollama** model — fully local rebuilds included.
Synthesis takes up to a minute; the dialog shows progress and quitting
mid-synthesis takes a deliberate second `ctrl+c`.

Deliberately, the model does **not** fabricate per-turn responses:
invented specifics dressed as history would poison every future resume.
The brief is orientation, explicitly hedged, and clearly marked with
the model that wrote it (`[AI-reconstructed context — inferred … by
opus · claude; the original responses were lost and are NOT
recovered]`). The audit records which model was used — and records
failures too (`rescue-failed` entries with the error), so a rescue
that went wrong is diagnosable after the fact.

Both rebuild flavors mint independent sessions — you can run each on
the same lost session and keep whichever resumes better.

## Lineage: originals and rebuilds know each other

Reconstructions are permanently mapped to their original through the
audit log:

- The original shows `✕ rescued` while at least one rebuild exists
  (never for stub files an agent may leave behind), and stays in the
  lost facet — the designation is permanent.
- `o` on a rescued original resumes its newest rebuild directly, or
  opens a chooser when several exist. The original itself is never
  resumable.
- `r` on a rescued original lists the existing rebuilds first and
  warns that rebuilding again creates another session alongside.
- The inspector gives lost sessions a **Recovery** tab (in place of
  Usage, which is meaningless without a transcript): every
  reconstruction with live state and location, the export artifact if
  one exists, and the session's full audit history. A rebuilt
  session's overview names its original.

Codex rebuild is not yet supported (synthesizing the codex transcript
format safely is its own project); codex lost sessions offer export
only.

## Badge semantics: transient signal vs permanent record

The STATE column answers *"what does this session need from me right
now?"* — so `✚ restored` yields once the session sees new live writes:
operationally it's just a healthy session again. Nothing is forgotten,
though; the record survives in three permanent layers:

1. **The audit log** (`audit.log` in the backup root) — append-only,
   never pruned: every restore, export, rebuild, transplant, and
   failed rescue with timestamps and paths.
2. **The inspector** — a restored session's overview keeps its
   `restored` line for life; lost sessions get the Recovery tab.
3. **The activity pane** (`m` → Activity) — the full action history,
   in the app.

`⟳ rebuilt` and `✕ rescued` are the deliberate exceptions: they stay
on the row permanently, because a rebuild isn't an event that happened
to a session — it's what the session *is*, and the original's loss is
part of its identity.
