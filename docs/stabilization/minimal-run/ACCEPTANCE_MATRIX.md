# ACCEPTANCE_MATRIX.md — Minimal Reliable Run

Every requirement (`FR-*`, `I-*`, `NFR-*`), every outcome, and every known
failure (`KF-*`) is mapped here to its proof (test / gate) and pass criteria.

**Initial status of every row is `NOT IMPLEMENTED`.** The implementing agent
flips a row to `PASS` **only** when the cited proof is green and reviewed; the
reviewer (Gate E) may downgrade any row back to `FAIL` if the proof is weak
(see `REVIEW_CHECKLIST.md`).

Legend:
- `NOT IMPLEMENTED` — the work for this row has not been done.
- `PARTIAL` — some code exists but a cited test is missing or red.
- `PASS` — cited proof is green and the reviewer accepted it.
- `FAIL` — a cited test exists and is red, or the reviewer rejected the proof.

---

## 1. Functional requirements

| ID    | Requirement (one-line)                                  | Proof              | Pass criteria                                                  | Status         |
|-------|---------------------------------------------------------|--------------------|----------------------------------------------------------------|----------------|
| FR-1  | Resolve current Git repo from `$PWD`                    | B-08 (not-a-repo)  | exit 2 outside repo; resolves inside repo                      | NOT IMPLEMENTED|
| FR-2  | Daemon autostart (find/spawn/ready/no-dual/stale)       | B-09, B-10, B-11   | one pid; readiness; stale reclaimed; no dual                   | NOT IMPLEMENTED|
| FR-3  | Create task from description                            | B-01               | task id in JSON; row in DB                                     | NOT IMPLEMENTED|
| FR-4  | Isolated worktree; primary untouched                    | U-10, B-12, B-13   | worktree under home; primary snapshot identical                | NOT IMPLEMENTED|
| FR-5  | Launch one production adapter (opencode) in worktree     | B-01, Smoke        | engine=opencode; allowlisted env (AC-28)                       | NOT IMPLEMENTED|
| FR-6  | Prompt reaches adapter verbatim                         | U-08               | byte-for-byte equality                                         | NOT IMPLEMENTED|
| FR-7  | Model reaches adapter verbatim                          | U-09               | argv contains exact `--model <id>`                             | NOT IMPLEMENTED|
| FR-8  | Wait for one terminal event                             | B-01, B-04, B-06   | blocks until terminal/timeout                                  | NOT IMPLEMENTED|
| FR-9  | Post-run Git inspection (4 commands)                    | U-01               | actualHEAD/status/diff read from worktree; cached sha ignored  | NOT IMPLEMENTED|
| FR-10 | Persist actual HEAD as head_sha                         | U-01, B-01         | head_sha == `git rev-parse HEAD`                               | NOT IMPLEMENTED|
| FR-11 | Outcome classification (pure function)                  | U-02               | all §1.1 cells exact                                           | NOT IMPLEMENTED|
| FR-12 | Terminal workspace state (never active after run)       | U-03, U-11, B-01   | state ∈ {completed,failed,cancelled,timed_out}                 | NOT IMPLEMENTED|
| FR-13 | Terminal task state                                     | B-01, B-02         | COMPLETED/FAILED/CANCELLED per outcome                         | NOT IMPLEMENTED|
| FR-14 | Result ref under refs/heads/..., idempotent             | U-04, U-05, B-01   | full ref form; idempotent; no remote                           | NOT IMPLEMENTED|
| FR-15 | Cancellation precedence; timeout != cancel              | U-06, U-07, U-15, B-05, B-06 | cancelled wins; timeout = timed-out                  | NOT IMPLEMENTED|
| FR-16 | Process-tree termination                                | B-05               | no orphan child                                                | NOT IMPLEMENTED|
| FR-17 | Restart preserves terminal; marks stale active failed   | U-12, B-14, B-15   | terminal unchanged; active→failed                              | NOT IMPLEMENTED|
| FR-18 | Clear CLI result + exit code (human + JSON)             | U-14, B-01..B-08   | OUTCOME_CONTRACT §2/§3/§4                                      | NOT IMPLEMENTED|
| FR-19 | LOCAL_REVIEW wall (no network, no creds)                | U-13, B-12         | zero network ops; no remote refs; allowlisted env              | NOT IMPLEMENTED|
| FR-20 | No paid model in default tests                          | (suite-wide)       | smoke is the only opt-in test; default suite offline           | NOT IMPLEMENTED|

---

## 2. Invariants

| ID   | Invariant                                    | Proof                          | Pass criteria                                       | Status         |
|------|----------------------------------------------|--------------------------------|-----------------------------------------------------|----------------|
| I.1  | Process success ≠ task success               | U-02, B-02                     | no-change run is failure                            | NOT IMPLEMENTED|
| I.2  | Git is source of truth after run             | U-01, B-01                     | head_sha from `rev-parse HEAD`                      | NOT IMPLEMENTED|
| I.3  | Outcomes disjoint and total                  | U-02                           | every terminal maps to exactly one outcome          | NOT IMPLEMENTED|
| I.4  | No-change run ⇒ non-zero exit                | B-02                           | exit 1; `completed-no-changes`                      | NOT IMPLEMENTED|
| I.5  | Committed result carries real commit SHA     | B-01, B-13                     | commit_sha == actual_head_sha; ref → it             | NOT IMPLEMENTED|
| I.6  | Uncommitted result preserved, honestly labelled | B-03                        | changed_files non-empty; commit_sha null            | NOT IMPLEMENTED|
| I.7  | Result refs only under refs/heads/...        | U-05, B-01                     | full form; idempotent                               | NOT IMPLEMENTED|
| I.8  | Terminal states absorbing                    | U-11, U-12, B-14, B-15         | no terminal→active; restart safe                    | NOT IMPLEMENTED|
| I.9  | Cancellation precedence                      | U-06, U-07, U-15, B-05, B-06   | cancelled wins; timeout≠cancel; race-clean          | NOT IMPLEMENTED|
| I.10 | LOCAL_REVIEW hard wall                       | U-13, B-12                     | no network; no creds to agent                       | NOT IMPLEMENTED|
| I.11 | `--json` is one document                    | U-14, B-07                     | one valid JSON object + newline                     | NOT IMPLEMENTED|
| I.12 | Primary checkout never modified              | U-10, B-12, B-13               | HEAD + file set identical                           | NOT IMPLEMENTED|

---

## 3. Non-functional requirements

| ID    | Requirement                          | Proof                                  | Pass criteria                                          | Status         |
|-------|--------------------------------------|----------------------------------------|--------------------------------------------------------|----------------|
| NFR-1 | Cold autostart < 12s; warm < 5s      | B-09 timing assertion                  | upper-bound test passes                                | NOT IMPLEMENTED|
| NFR-2 | Classifier is pure/deterministic     | U-02                                   | same inputs ⇒ same outcome                             | NOT IMPLEMENTED|
| NFR-3 | `-race` clean                        | Gate B (`go test -race -count=1 ./...`)| 0 races                                                | NOT IMPLEMENTED|
| NFR-4 | Default suite offline                | suite-wide                             | no net/paid calls except opt-in smoke                  | NOT IMPLEMENTED|
| NFR-5 | gofmt/vet/diff-check/make check      | Gate A+B                               | all clean/green                                        | NOT IMPLEMENTED|
| NFR-6 | No silent fallback                   | B-02, B-04, B-08                       | failures loud + classified                             | NOT IMPLEMENTED|
| NFR-7 | No mass deletion                     | (diff review)                          | scheduler/failover/postmerge/review/merge pkgs remain  | NOT IMPLEMENTED|
| NFR-8 | Forward-only schema                  | (migration review)                     | new idempotent forward migration only                  | NOT IMPLEMENTED|

---

## 4. Outcome coverage

| Outcome                             | Test(s)                  | Status         |
|-------------------------------------|--------------------------|----------------|
| `completed-with-commit`             | U-02, B-01, B-13, Smoke  | NOT IMPLEMENTED|
| `completed-with-uncommitted-changes`| U-02, B-03, B-13         | NOT IMPLEMENTED|
| `completed-no-changes`              | U-02, B-02, B-13         | NOT IMPLEMENTED|
| `failed`                            | U-02, B-04, B-13         | NOT IMPLEMENTED|
| `cancelled`                         | U-06, U-15, B-05         | NOT IMPLEMENTED|
| `timed-out`                         | U-07, B-06               | NOT IMPLEMENTED|
| `interrupted`                       | U-12, B-14               | NOT IMPLEMENTED|

---

## 5. Known-failure regression coverage

Each confirmed defect in `KNOWN_FAILURES.md` must have a regression test that
fails on the old code and passes on the fixed code.

| ID   | Defect (short)                                  | Regression test            | Status         |
|------|-------------------------------------------------|----------------------------|----------------|
| KF-01| Process success mistaken for task success       | U-02, B-02                 | NOT IMPLEMENTED|
| KF-02| No post-run Git inspection                      | U-01                       | NOT IMPLEMENTED|
| KF-03| Workspace left `active` after run               | B-01, B-13                 | NOT IMPLEMENTED|
| KF-04| `head_sha` stays at base                        | U-01, B-01                 | NOT IMPLEMENTED|
| KF-05| Checkpoint points at base commit                | U-02 (no-change)           | NOT IMPLEMENTED|
| KF-06| Task stays `NEW`                                | B-01, B-02                 | NOT IMPLEMENTED|
| KF-07| Result ref not auto-created on run              | B-01                       | NOT IMPLEMENTED|
| KF-08| Result ref must be full `refs/heads/...` form   | U-05                       | NOT IMPLEMENTED|
| KF-09| Gemini cancellation race → run.failed           | U-06, U-15, B-05           | NOT IMPLEMENTED|
| KF-10| Usage not persisted via workspace-run path      | (new) usage-persist test   | NOT IMPLEMENTED|
| KF-11| No `forge run` command                          | B-01..B-08                 | NOT IMPLEMENTED|
| KF-12| Exit code misreports success                    | B-02, B-04                 | NOT IMPLEMENTED|

---

## 6. Review gates (Gate E signs off here)

| Gate | Description                                            | Status         |
|------|--------------------------------------------------------|----------------|
| A    | Static: gofmt / vet / diff --check clean               | NOT IMPLEMENTED|
| B    | Tests: `go test -count=1 ./...` + `-race` + `make check`| NOT IMPLEMENTED|
| C    | Reliability: 10 sequential green iterations + invariants| NOT IMPLEMENTED|
| D    | Real OpenCode smoke (opt-in, once)                      | NOT IMPLEMENTED|
| E    | Independent review (REVIEW_CHECKLIST.md)                | NOT IMPLEMENTED|

A row may only move to `PASS` after Gate E accepts it.
