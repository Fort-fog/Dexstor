# DEXSTER v32 ROTATION

This branch combines the full DEXSTER Web/Planner/Worker architecture with the proven v30 Context Rotation behavior. It contains no task-specific gallery pipeline or image-autopatch controller. General file tools, CAS, KRAKEN checkpoints/resume, Evidence Gate, HTML validation, repair diagnostics, Worker isolation and Planner WriteScope remain available.

Context rotation keeps trusted CAS snapshots in state while sending only the active source file in full; older reads are represented compactly and may be re-read when relevant.

Автономный код-агент на Go: читает проект, вносит изменения, проверяет diff,
запускает тесты и завершает задачу только при полной цепочке доказательств.
Спроектирован как более простая альтернатива FSM-архитектуре bratan-go
(proetGO): вместо жёсткого автомата состояний — один цикл планирования с
политикой завершения (evidence gate) и песочницей инструментов.

## Архитектура

```
cmd/dexster          CLI: флаги, запуск агента
internal/llm         клиент Ollama (/api/generate) + парсер протокола TOOL_CALL
internal/tools       песочница: Read / Write(CAS+атомарно) / ListFiles / GitDiff / RunTests
internal/agent       контроллер: цикл, evidence gate, журнал, состояние и восстановление
```

### Ключевые решения (в сравнении с proetGO)

| Аспект | bratan-go (proetGO) | DEXSTER |
|---|---|---|
| Управление | FSM из 8 состояний со строгими переходами | один цикл; модель вызывает инструменты свободно, контроллер проверяет политику |
| Завершение | переходы TEST PASS / data-file ветки | evidence gate: `write -> git_diff -> run_tests PASS` (или `declare_done`, если тестовой цели нет), порядок по номерам итераций |
| Безопасность файлов | protected files + CAS + атомарные записи | то же ядро: sandbox-пути, защита `*_test.go`, CAS по sha256 снапшота чтения, temp+rename |
| Состояние | `.ai/task_state.json` + reconcile | `.dexster/state.json`: атомарная запись после каждого действия; corrupt -> карантин `*.corrupt-<ts>`; чужая задача -> `*.stale-<ts>` |
| Восстановление | resume по сохранённому состоянию | то же: повторный запуск с той же задачей продолжает с сохранённой итерации (включая CAS-снапшоты) |
| Промпт | состояния + файлы | задача, список файлов, содержимое прочитанных (`--- FILE:`), evidence, последние действия, LAST RESULT, динамические HINT/NEXT STEP |

### Evidence gate (условие DONE)

Задача завершается только если:
1. проект осмотрен (`list_files`/`read_file`);
2. есть хотя бы одна успешная запись;
3. `git_diff` выполнен ПОСЛЕ последней записи;
4. либо `run_tests` с PASS в выводе после последней записи (если в проекте есть
   тестовая цель: любой `*_test.go` или root-`.go` программа),
   либо `declare_done` после последней записи (только если тестовой цели нет).

Любая новая запись сбрасывает требование 3–4 (нужны повторные diff+test).

### Безопасность (не ослабляется)

- все пути резолвятся внутри `-root`; абсолютные пути и `..` отклоняются;
- `*_test.go` в корне проекта — защищённые оракулы, запись отклоняется до CAS;
- запись существующего файла требует base-hash = sha256 последнего прочитанного
  содержимого (CAS); устаревший снапшот -> отказ; новый файл пишется без hash;
- записи атомарные: temp-файл в том же каталоге + `rename`, без мусора при ошибках;
- `go.mod` создаётся только внутри root (никогда не выше);
- завершение невозможно без доказательств — модель не может «сказать, что всё
  сделано» для проекта с тестами.

## Запуск

Требования: Go >= 1.22, локальный Ollama (проверено на `http://localhost:11434`,
модель по умолчанию `qwen3:8b` — та же конфигурация, что у proetGO).

```sh
cd DEXSTER
go build -o /tmp/dexster-bin ./cmd/dexster

/tmp/dexster-bin \
  -root /path/to/project \
  -task "Fix math.go so Add(2, 3) returns 10" \
  [-model qwen3:8b] \
  [-ollama-url http://localhost:11434] \
  [-max-iterations 20]
```

Повторный запуск с той же задачей и тем же root:
- задача уже DONE -> немедленный выход без запросов к модели;
- прервана на середине -> возобновление с сохранённой итерации.

Состояние и журнал: `<root>/.dexster/state.json` (evidence, journal, снапшоты чтений).

## Тесты

```sh
gofmt -l .            # пусто
go build ./...
go vet ./...
go test ./... -count=1
```

Покрытые сценарии: парсер TOOL_CALL (мультистрочный content, JSON-квотинг,
отказ при множественных блоках); sandbox (traversal, absolute paths), защита
`*_test.go`, CAS (нет снапшота / устаревший / валидный), атомарность без
temp-мусора, diff-fallback без git, `go test` и standalone-программы; e2e с
мок-Ollama: полный repair-flow до DONE за 4 запроса, восстановление из corrupt
состояния (карантин + fresh run), ограниченность при зацикливании модели на
повторных чтениях, отказ гейта без diff/test-доказательств, защита оракула на
уровне агента, идемпотентный перезапуск после DONE, возобновление после
прерывания (CAS-снапшот переживает рестарт), data-file задача через
`declare_done`.

## Известные ограничения

- нет общего shell-инструмента (`exec`) — только декларативные инструменты;
  произвольные команды намеренно исключены для сокращения поверхности атаки;
- защита `*_test.go` действует в корне проекта (как и glob-защита bratan-go);
- при совпадении хэша задачи DONE-состояние доверяется без повторной проверки
  файловой системы (reconcile bratan-go не переносился — осознанное упрощение);
- `run_tests` для standalone-программ ожидает маркер PASS в выводе (контракт
  фикстур, как в proetGO);
- качество автономной работы зависит от модели: на qwen3:8b задача из smoke-теста
  решается за ~7 итераций с 1–2 отклонёнными CAS-записями; слабее модели могут
  исчерпать бюджет (агент честно останавливается с ошибкой, не подменяя результат).

## Smoke-тест с реальной моделью (фактический вывод)

```
[DEXSTER] root=/tmp/dexster-smoke model=qwen3:8b max-iterations=15
[DEXSTER] #1 read_file math.go ok=true
[DEXSTER] #2 write_file math.go ok=true
[DEXSTER] #3 write_file math.go ok=false      <- CAS отклонил запись без перечитывания
[DEXSTER] #4 read_file math.go ok=true        <- HINT заставил перечитать
[DEXSTER] #5 git_diff math.go ok=true
[DEXSTER] #6 run_tests math_test.go ok=true   <- go test: --- PASS: TestAdd
[DEXSTER] DONE after 7 iterations
```

Результат: `func Add(a, b int) int { return (a + b) * 2 }` — тесты проходят.

## KRAKEN event logging (optional)

DEXSTER and KRAKEN remain separate Go modules. From the `DEXSTER` directory,
pass the KRAKEN repository root explicitly:

```bash
go run ./cmd/dexster -root /path/to/target-project \
  -task 'Describe the coding task' -model qwen3.8:27b-iq4xs \
  -kraken-root ../KRAKEN
```

`-kraken-root` is optional: without it DEXSTER behaves as before. When enabled,
DEXSTER appends individual JSON records to `KRAKEN/events/` for task start,
resume, tool actions, code changes, test results and completion. A failed event
write stops the run rather than silently hiding the failure. KRAKEN does **not**
currently create Git checkpoints, perform rollback or feed memory back to the
model. DEXSTER's existing `.dexster/state.json` remains the resume authority.

### KRAKEN integration

Enable the optional sibling Go module with `-kraken-root ../KRAKEN`
(or an absolute path). With the integration enabled, KRAKEN stores append-only
run events, private pre-write checkpoints, and up to four human-reviewed,
project-scoped lessons in the model prompt. No automatic rollback or lesson
creation is performed. See `../KRAKEN/README.md` for snapshot safety and the
lesson JSON format. Tests use simulated Ollama responses; they do not prove
that a live local model adheres to the tool-call protocol.

## Cold-context continuation (v6)

DEXSTER's Ollama requests are stateless (`/api/generate`, no `context` array):
there is no conversation to carry across calls. The growing prompt was the
problem. v6 caps the *estimated* prompt size and switches to a compact view
that omits full read-file snapshots from the model prompt; the CAS snapshots
remain in `.dexster/state.json`. The compact prompt includes `AGENTS.md`
(up to 2 KiB) and the verified KRAKEN resume hint. The agent re-reads files
before changing them if needed. KRAKEN's `tasks/<id>.json` includes a small
`resume` capsule tied to the SHA-256 of the exact DEXSTER state journal. On
resume, a mismatching same-iteration capsule causes a safe stop.

Example (the external Ollama model name is supplied at runtime):

```sh
go run ./cmd/dexster -root /your/code -kraken-root ../KRAKEN \
  -model YOUR_OLLAMA_MODEL -num-ctx 16384 -prompt-budget 9000 \
  -task 'your specific coding task' -max-iterations 40
```

`-prompt-budget` is a heuristic based on UTF-8 bytes / 4, **not** Ollama's
actual token count. For Cyrillic or dense code, use a conservative budget.
If even the compact prompt exceeds the bound, the agent stops without asking
the model to work with a truncated prompt. This release does not unload
Ollama's model weights or implement automatic long-task summarization.
The mock tests cover five restarts, a forged-state mismatch, and rotation
before forwarding a large prompt; live Qwen testing remains necessary.

## Measured Ollama cold rotation (v7)

v7 reads **actual** `prompt_eval_count` and `eval_count` from the
non-streaming Ollama `/api/generate` response. When their sum reaches
`-rotate-at`, DEXSTER first persists its state and KRAKEN acknowledgment,
updates the resume capsule, then optionally calls `/api/generate` with an
empty prompt and `keep_alive: 0` to unload the model. The next request
uses the compact cold prompt. No old conversational context is sent.

```
go run ./cmd/dexster -root /path/to/target-project \
  -kraken-root ../KRAKEN -model YOUR_OLLAMA_MODEL \
  -num-ctx 16384 -rotate-at 13926 -unload-on-rotate=true \
  -prompt-budget 9000 -task 'your task' -max-iterations 40
```

`-rotate-at 0` selects 85% of `num_ctx`. The byte-estimate prompt guard
still protects the *first* request (telemetry is available only **after**
Ollama processes it). A server that omits token counts cannot trigger
telemetry rotation; the conservative prompt guard still applies. An Ollama
response with `done_reason=length` stops safely instead of executing a
possibly truncated tool call. A failed unload also stops the run after
persisting state rather than proceeding silently. Unloading weights is
optional (`-unload-on-rotate=false`), because Ollama requests here are
already independent; keep-alive zero is **not** a restart of the service.

HTTP mock tests verify request metadata, `keep_alive: 0`, the ordering of
KRAKEN acknowledgement before unload, a full task after one rotation, and
repeated cold rotations. A live GPU/Ollama integration test is still required.

## v8 verification reliability (October 2026)

The CLI defaults to `-auto-verify=true`: once a write succeeds, DEXSTER's
**Go controller**, rather than Qwen, selects `git_diff` and `run_tests` on the
following iterations. Each check is journaled and acknowledged by KRAKEN.
A failed test is returned to the model; no further test runs automatically
until another successful write occurs. Use `-auto-verify=false` to retain
legacy model-selected verification. The controller still requires ordinary
completion evidence and does not auto-commit or push.

If Ollama produces invalid responses, DEXSTER reports `done_reason`, prompt
and generation token counts, thinking character count and an excerpt from
`response` for diagnosis. A model response without a valid `TOOL_CALL` remains
rejected; it is never interpreted as a filesystem command.

For an **existing test project** whose state reached iteration 20, restart
with the exact same task and `-max-iterations 40` (or larger); this resumes
`.dexster/state.json` and does not reset the journal. If Qwen already wrote
source code in iteration 13, controller verification can resume on the next
iteration without making Qwen choose the verification tools.

This update has mock-Ollama tests only. It must be tested against the real
Qwen model before claiming reliable live tool-call parsing.

## v9: verified mock reliability pass

- `TOOL_CALL` requires exactly one complete block with a line-anchored terminator.
  A single JSON object such as `{"name":"read_file","arguments":{"path":"main.go"}}`
  is also accepted; multiple calls remain forbidden.
- Incomplete Ollama generations (`done=false`), truncation (`done_reason=length`),
  and non-2xx HTTP responses fail closed, rather than applying partial writes.
- Automatic verification runs `git_diff` and `run_tests` in the Go controller.
  The prompt no longer contradicts that behavior.
- Nested `*_test.go` files are protected; filesystem operations and KRAKEN
  pre-write snapshots reject paths through symlink components.
- Tests cover intermittent malformed model output, repeated process restarts,
  symlink attempts, incomplete Ollama responses and existing cold rotations.

**Known limits:** these are mock tests, not a live Qwen benchmark; the cause of
Qwen's original `parse rejected` replies still needs raw-response sampling.
The context counter is per request, not a running sum. `keep_alive: 0` unloads
model weights but does not magically improve a malformed response. A group
rollback is not yet atomic against power failure or concurrent processes;
exercise it in a disposable repository before relying on it for production.
Do not enable automatic Git commits or destructive rollback by default.

### Explicit constraint gate (v11)

Use repeatable `-protect` flags to enforce machine-checkable prohibitions:

```bash
# Preserve a Go function or method while allowing other functions to change.
-protect main.go:Multiply
# Freeze an entire file byte-for-byte.
-protect main_test.go
```

A protected Go declaration is compared using a canonical Go AST rendering; changes to its signature, receiver, or body are rejected before any write or KRAKEN checkpoint. The gate verifies protected files again before `DONE` and on task resume. Rules and baseline fingerprints are persisted in the task's `.dexster/state.json`; a restarted task must not supply different protected rules. Existing tasks with no baseline cannot add protections retroactively: start a separate task in a clean fixture.

**Important:** Natural-language instructions alone are *not* machine-enforced. Specify important non-edit requirements with `-protect`. `-protect` is not a semantic correctness test; for example, an unrelated function returning a constant can still pass weak tests. The check does not offer an OS sandbox against concurrent hostile writes; use trusted project directories.

## v12: Git Quality Gate (opt-in)

CLI flag `-auto-commit=true` creates a **local-only** Git commit after the
normal evidence gate succeeds (`git_diff`, tests PASS if applicable, and
Constraint Gate). It does not push and is **disabled by default**.

Safety conditions:
- The target `-root` must be exactly the top-level of an initialized Git repo.
- Git user.name / user.email must already be configured.
- The Git index must be empty of staged changes before DEXSTER stages its files.
- Only paths from successful `write_file` actions for this task are staged;
  unrelated untracked/modified files are not included in the commit.
- Invalid constraints / failed tests / missing evidence never invoke commit.
- Commit receipt is stored in `.dexster/state.json` as `git_commit`.
- A trailer (`DEXSTER-Task-SHA256`, `DEXSTER-Paths`) in HEAD avoids a duplicate
  commit if the process dies after `git commit` but before saving the receipt.
- Errors leave the project file contents intact and unstage only task paths;
  never run a hard reset or push.

Example (on a separate test Git repository):

```sh
/path/to/dexster -root /tmp/git-quality-test -kraken-root ../KRAKEN \
 -model qwen3.8:27b-iq4xs -num-ctx 16384 -prompt-budget 9000 \
 -auto-verify=true -auto-commit=true -max-iterations 15 \
 -protect main_test.go -task 'Fix Add to add two integers; leave tests unchanged.'
```

Known boundaries: Git commit does not provide transactional power-loss
atomicity; this uses Git's ordinary commit durability. No automatic push,
branch creation, or rebase. The old v11 defaults are unchanged.

## v14: project-specific quality gate (opt in)

Run `dexster -verify-go-tags dexster_extra -auto-verify=true ...` to require
**both** regular `go test` and `go test -count=1 -tags dexster_extra ./...`
before `DONE` and before any automatic Git commit. Add a protected test
file such as `edge_cases_test.go` starting with `//go:build dexster_extra`.
Extra tests must be inside the project and are visible to the agent; they
are not the external hidden benchmark tests. It fails closed when no tagged
test file exists; a failing extra check is reported to the model for repair.
Successful verification is invalidated by each new write. The verification
tag is stored in `.dexster/state.json` and cannot change across resume.
This runs **Go tests as local code**, not in a security sandbox. Only run on
trusted projects. It does not prove absence of undiscovered bugs.

### v31 HTML checks (pure Go, zero external runtime dependencies)

When `-auto-verify=true`, changed `.html` files are checked after `git_diff`
and before `DONE` by `internal/tools/html_validator.go` using only the Go
standard library. Failures return to the model for another repair attempt.

The checker catches unmatched HTML tags, unclosed comments and missing
project-local image files, and rejects image paths that escape the project.
It is deliberately a **structural checker, not a complete HTML5 standards
validator or browser renderer**. Some valid HTML with optional end tags or
special parsing rules may require additional handling; the checker does not
prove visual appearance or full task completion.

No Java, Perl, Node, external binaries, or third-party Go packages are needed.
The preexisting structure-preservation guard, CAS, KRAKEN and task-state gates
are retained.

This is a separate candidate. Do not replace a stable deployment without
running the user's actual site and model against it.