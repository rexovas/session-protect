# Recovery: restore, rescue, and what each state means

SessionProtect distinguishes four recovery-related states that sound
similar but are mechanically very different.

| State | Meaning | What still exists | Remedy |
| --- | --- | --- | --- |
| `✝ recover` | Transcript deleted from the agent's storage, **but present in backup** | The full transcript, in the backup repo | `r` → **Restore** |
| `✕ lost` | Transcript gone from disk **and** backup — deleted before protection existed | Only your prompts, in the agent's own history file | `r` → **Rescue** (export or rebuild) |
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

## Rescue tier 1 — Export prompts

`r` on a lost session → **Export prompts** writes a markdown artifact
to `<backup root>/exports/<session-id>.md`: project, prompt count and
time span, then every surviving prompt with its timestamp — plus an
honest header noting that only the human side survives. Works for both
agents. Use it to salvage the thinking: context for a new session,
material for a doc, whatever you need.

## Rescue tier 2 — Rebuild (claude)

`r` → **Rebuild** synthesizes a **new, genuinely resumable session**:

- Your prompts become user messages, each followed by an assistant
  placeholder `[response lost]`; the final placeholder tells the
  resumed agent its situation — that the original transcript was lost
  and the conversation continues from the surviving prompt history.
- The file is structurally a real transcript (message chain, real
  timestamps from history) written into the project's storage under a
  **fresh session id**, created exclusively so it can never overwrite
  anything. The format was validated against a live resume before this
  feature shipped.
- The rebuild is synced to backup immediately, so it is protected from
  the moment it exists.
- Responses are *not* recovered (they are gone) and *not* invented —
  the placeholders are honest.

## Rescue tier 3 — Rebuild with AI (claude)

**Rebuild with AI** goes one step further: a model of your choice
(↑/↓ selects — claude aliases with opus as the default, or any
installed ollama model) reads the full prompt sequence and writes a
*reconstruction brief* — the session's inferred goal, the arc of the
work, decisions the prompts imply, and the open threads — which becomes
the final message of the new session.

Deliberately, the model does **not** fabricate per-turn responses:
invented specifics dressed as history would poison every future resume.
The brief is orientation, explicitly hedged, and clearly marked with
the model that wrote it (`[AI-reconstructed context — inferred … by
opus · claude; the original responses were lost and are NOT
recovered]`). The audit records which model was used.

Both rebuild flavors mint independent sessions — you can run each on
the same lost session and keep whichever resumes better.

Afterward: open a rebuilt session with `o` and the agent resumes with
your full prompt history as context, aware it's a reconstruction. The
original stays marked `✕ lost` forever, and the audit log links the two.

Codex rebuild is not yet supported (synthesizing the codex transcript
format safely is its own project); codex lost sessions offer export
only.

## Badge semantics: transient signal vs permanent record

The STATE column answers *"what does this session need from me right
now?"* — so `✚ restored` yields once the session sees new live writes:
operationally it's just a healthy session again. Nothing is forgotten,
though; the record survives in three permanent layers:

1. **The audit log** (`audit.log` in the backup root) — append-only,
   never pruned: every restore, export, rebuild, and transplant with
   timestamps and paths.
2. **The inspector** — a restored session's overview keeps its
   `restored` line for life, driven by the audit log.
3. **The activity pane** (`m` → Activity) — the full action history,
   in the app.

`⟳ rebuilt` is the deliberate exception: it stays on the row
permanently, because a rebuild isn't an event that happened to a
session — it's what the session *is*.
