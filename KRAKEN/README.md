# KRAKEN

Internal memory and checkpoint repository for DEXSTER agent.

Purpose:
- store agent task history
- store checkpoints
- store decisions
- store lessons from successful and failed runs
- provide rollback and recovery history

KRAKEN is not the project source repository.
It is the agent memory and execution history layer.

## Go event store

`memory` is a Go package in this repository (`module kraken`). DEXSTER imports
it using a local `replace kraken => ../KRAKEN` declaration. When configured
with `-kraken-root`, DEXSTER writes unique, append-only JSON event files in
`events/`. These event files are local run data; do not commit them without
reviewing task descriptions, paths and tool output, which can be sensitive.

KRAKEN now provides checkpoint creation, crash reconciliation, guarded rollback,
verified project snapshots, retention and reviewed lesson injection. Git baselines
remain local-only safety anchors; rollback never requires a remote service.

## Integrated memory (Go)

DEXSTER can connect with `-kraken-root /absolute/path/to/KRAKEN`.

* `events/`: append-only run events, emitted after DEXSTER persists actions.
* `checkpoints/`: private (`0600`) pre-write file snapshots, one per changed
  file write (including a marker when the file did not previously exist).
  These are **not** Git commits and never restore files automatically.
  Snapshots may contain source secrets; keep the directory private and out of
  shared backups. Old snapshots are not yet pruned automatically.
* `lessons/`: optional human-reviewed JSON notes. DEXSTER loads at most four
  approved notes for the exact resolved project path into the prompt. Notes
  are reference material, not executable instructions or permission changes.

Example `lessons/project-guide.json` (replace `/absolute/project` with your
project root; do not add unreviewed model output as an approved lesson):

```json
{
  "project": "/absolute/project",
  "title": "Testing convention",
  "note": "Run the existing unit tests after changing source files.",
  "approved": true
}
```

Limits: per-write checkpoints preserve pre-write bytes and verified snapshots
preserve completed project states. Automatic lesson promotion is intentionally
not performed. A live Ollama model is not used by the automated integration tests.

## Controlled rollback (v3)

Checkpoints are first written as **unarmed** pre-write snapshots. After a
successful DEXSTER write, KRAKEN stores the expected SHA-256 of the resulting
file. Only armed snapshots can be restored. To preview a restore:

```sh
cd KRAKEN
# supply an actual file from KRAKEN/checkpoints/*.json
 go run ./cmd/kraken -root "$PWD" -project /absolute/project -checkpoint /absolute/KRAKEN/checkpoints/FILE.json
```

Review the preview and use `-apply` to restore that single file. The restore
refuses if the project does not match, the snapshot is damaged/unarmed, the
current file has been changed since the recorded write, or a symlink occurs
in the source path. Files first created by DEXSTER are deleted on rollback.
It does **not** run `git reset`, `git checkout`, or roll back an entire task.
This is a CAS-guarded per-file restore, not a transactional multi-file rollback.
Do not use it while other processes are modifying the same project: filesystem
races and power-loss durability of parent directories are not yet handled.

## Local Git baseline and guarded group rollback (v4)

KRAKEN uses the project's **local Git repository**, without any remote or API.
For a clean Git worktree, pin its current commit to a task-specific, immutable
Git ref (no commit, stage, checkout, or branch switch):

```sh
cd KRAKEN
# Works only when /absolute/project is the Git worktree root and is clean.
go run ./cmd/kraken -project /absolute/project -pin-git-baseline 'task-id'
```

The resulting `refs/kraken/baselines/<hash>` pins the HEAD commit against garbage
collection. This is a baseline of committed content, **not** a backup of
uncommitted files. KRAKEN explicitly refuses a dirty tree.

To preview a grouped restore from existing, *armed* per-file checkpoints:

```sh
go run ./cmd/kraken -root /absolute/KRAKEN -project /absolute/project \
  -task-checkpoints '/absolute/KRAKEN/checkpoints/a.json,/absolute/KRAKEN/checkpoints/b.json'
```

Add `-apply` to restore. All paths and post-write hashes are checked before the
first change; duplicate files, symlinks, foreign projects and concurrent changes
are rejected. **Multi-file application is not crash-atomic:** a late I/O error
or concurrent modification may leave some files restored. The CLI reports a
partial rollback explicitly. Keep local Git history as the durable source of
committed states; never use `git reset --hard` or `git add -A` for this operation.

## Checkpoint maintenance (v15)

Completed task checkpoints may be pruned after a retention period. By default
nothing is removed: the command is a preview. Task manifests, event history,
lessons and decisions are never deleted by this command. Incomplete tasks and
pending writes are always protected. Standalone checkpoints (not referenced by
any task manifest) are left untouched.

**Stop DEXSTER agents that share this KRAKEN root before applying cleanup.**
The cleanup is not a concurrent GC and does not have a global process lock.
Once deleted, completed-task checkpoints cannot be used for file rollback;
back up the KRAKEN directory first if that rollback history matters.

From `KRAKEN/`:

```sh
go run ./cmd/kraken -root "$PWD" -cleanup -retention-days 30
# Only after reviewing candidates and making a backup:
go run ./cmd/kraken -root "$PWD" -cleanup -retention-days 30 -apply
```

Invalid manifest/checkpoint references fail closed. Cleanup first updates the
completed task manifest to remove references and then deletes its checkpoint
files. If interrupted after the manifest update, orphan files can remain for
manual inspection; they are not automatically deleted. There is no recurring
automatic deletion in v15.

## v16 interrupted cleanup recovery

Cleanup now records a durable `.cleanup-pending.json` intent before changing
manifest references. If cleanup stops unexpectedly, *stop all writers sharing
this KRAKEN root*, back up the store and run:

```bash
go run ./cmd/kraken -root "$PWD" -recover-cleanup -apply
```

This command validates task ownership and checkpoint hashes, removes any
remaining references before unlinking, and can be rerun safely. Ordinary
`-cleanup` previews refuse to proceed while a pending intent exists.
Do not manually delete the intent. A changed checkpoint or corrupt manifest
requires operator investigation rather than automatic deletion.

## Project rollback and VERIFIED states (v32)

Rollback is a KRAKEN/controller operation, not an LLM repair task. DEXSTER
recognizes explicit commands such as `откатись до рабочей версии`,
`верни предыдущую рабочую версию` and `верни первую рабочую версию` before
constructing a model client. The rollback path therefore reports `LLM calls=0`.

After every normal task reaches the Evidence Gate and DONE, KRAKEN stores a
private compressed VERIFIED source snapshot under `verified/` and attaches its
hash to the task manifest. `.git`, `.dexster`, `.kraken`, node_modules and cache
directories are excluded. The project's Git branch and index are not changed.

Selection rules:

- ordinary rollback / previous working version: if the current tree differs from
  the newest VERIFIED state, restore that newest state; if it already equals the
  newest state, restore the preceding VERIFIED state;
- first working version: restore the earliest VERIFIED state for the project;
- old pre-v32 histories without VERIFIED snapshots: reverse the existing armed
  pre-write checkpoint chain, including repeated writes to the same file.

Before applying any project rollback, KRAKEN captures a full safety snapshot of
the current source tree. If restoration fails part-way, it attempts to restore
that safety snapshot automatically. On success `.dexster/state.json` is archived
with a `.rollback-*` suffix so a later run cannot accidentally resume the state
that was just rolled back. Task/event/checkpoint history is retained.

Manual preview/apply is also available:

```sh
go run ./cmd/kraken -root /absolute/KRAKEN -project /absolute/project \
  -project-rollback previous
go run ./cmd/kraken -root /absolute/KRAKEN -project /absolute/project \
  -project-rollback previous -apply
```

Use `-project-rollback first` for the first recorded working state.
