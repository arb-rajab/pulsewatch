# Evidence Preservation — Why This Practice Exists
> Purpose: record why a same-day re-verification pass was needed after
> Session 13, so future sessions don't repeat the same gap.
> Last updated: 2026-09-01

## What happened

Session 13's handoff (`12-session-handoff.md`) narrated three real,
actually-performed pieces of verification work — the rollup cadence
root-cause investigation, the extended-observation window proving the fix
doesn't false-positive, and a real (non-mock) authenticated capture — in
enough detail that they read as trustworthy. They *were* real when
performed. But nothing beyond that prose survived: the containers involved
were recreated afterward, and no raw log excerpt, capture file, or diff was
ever saved to disk. When asked for the literal evidence rather than the
narrative, there was nothing to hand over.

## Why this matters

An accurate description of something you saw is not the same as having
kept the thing you saw. This project's own standing rule — ask for literal
evidence, not narrative — exists precisely because narrative degrades
silently: it can be perfectly honest at write-time and still become
unverifiable a day later for a reason that has nothing to do with honesty
(state moved on, a container got recreated, a log rotated). A handoff
document is not evidence storage; it's a pointer. Without a real file to
point to, it's just a claim with more words.

## The practice change

Starting with the 2026-09-01 re-verification pass: any session that
captures live system output as proof of a claim (log excerpts, curl
captures, build/deploy output, config diffs) saves that output to a file
under `docs/project-memory/evidence/` **as it is captured** — piped or
redirected directly (`| Tee-Object`, `> file.log`, `docker logs -f >
file`), not retyped from memory afterward — before the underlying state
(a container, a log stream, a temporary config change) can move on or get
cleaned up. Session handoffs and risk-register entries reference these
files by path instead of restating conclusions as if self-evidently true.

This is not a call to save everything forever — most evidence here is
dev-machine-specific and will stop being interesting once superseded. It's
a call to make the *next* verification request answerable with a file, not
a re-run.

## Related

- [[12-session-handoff]] — the re-verification pass this note explains
- `docs/project-memory/evidence/` — where the actual artifacts live
