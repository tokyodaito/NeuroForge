# NeuroForge

## Техническое задание на автономную мультимодельную фабрику разработки

**Версия:** 2.0
**Статус:** основной источник требований
**Тип продукта:** local-first CLI/TUI-приложение
**Рабочее имя исполняемого файла:** `forge`
**Основной язык реализации:** Go
**Основное хранилище:** SQLite
**Целевые ОС:** Linux (каноническая); запуск с Windows-хоста — через WSL2; macOS — generic Unix, best-effort (без отдельной поддержки)

---

# 1. Назначение продукта

NeuroForge — локальная система управления AI-разработкой, которая позволяет пользователю:

1. зарегистрировать один или несколько программных проектов;
2. включить фабрику для выбранных проектов;
3. добавлять задачи свободным текстом;
4. прикладывать изображения, документы, логи и другие материалы;
5. выбирать уровень автономности;
6. наблюдать за работой агентов через интерактивный терминальный интерфейс;
7. получать готовый код:

   * только в локальной ветке;
   * в удалённой ветке;
   * в Pull Request или Merge Request;
   * автоматически в целевой ветке.

NeuroForge самостоятельно управляет:

* выбором coding agent;
* выбором модели;
* квотами;
* расходом токенов;
* параллельными задачами;
* Git worktree;
* промежуточными checkpoint;
* тестированием;
* AI-review;
* визуальной проверкой UI;
* provider failover;
* созданием PR/MR;
* merge;
* post-merge проверкой;
* автоматическим откатом.

Пользователь управляет не отдельными агентами, а:

* backlog;
* политиками проекта;
* бюджетами;
* стадиями pipeline;
* уровнем автономности;
* приоритетами.

---

# 2. Ключевая модель взаимодействия

## 2.1. Интерактивный режим

Команда:

```bash
forge
```

должна открывать полноэкранный терминальный интерфейс.

Пользователь не обязан запоминать команды.

Основные действия должны быть доступны через:

* навигацию стрелками;
* мышь;
* интерактивные кнопки;
* переключатели;
* формы;
* контекстные меню;
* командную палитру;
* slash-команды;
* горячие клавиши.

## 2.2. Неинтерактивный режим

Все важные действия должны также иметь обычные CLI-команды для:

* скриптов;
* CI;
* удалённого управления;
* автоматизации;
* диагностики.

Пример:

```bash
forge project start work-app
forge task add --project work-app "Исправить экран авторизации"
forge task list --json
```

TUI является основным пользовательским интерфейсом.

CLI-команды являются вторичным программным интерфейсом.

---

# 3. Основные пользовательские сценарии

## 3.1. Полностью автономный личный проект

Пользователь:

1. добавляет проект;
2. включает autonomous profile;
3. добавляет задачи;
4. фабрика пишет код, тестирует, создаёт PR и вливает изменения.

## 3.2. Рабочий проект с ручной проверкой

Пользователь:

1. запускает задачу;
2. агент работает в отдельной локальной ветке;
3. push запрещён;
4. PR/MR запрещён;
5. merge запрещён;
6. пользователь открывает diff;
7. принимает, дорабатывает или удаляет результат.

Фабрика не должна отправлять код, ветки, патчи или описание задачи во внешний Git-сервис, кроме передачи необходимого контекста выбранной AI-модели в соответствии с политикой проекта.

## 3.3. Рабочий проект с PR/MR, но без merge

Фабрика:

* реализует задачу;
* запускает разрешённые проверки;
* делает push;
* создаёт PR или MR;
* останавливается.

Merge выполняет человек.

## 3.4. Быстрое прототипирование без тестов

Пользователь может отключить:

* генерацию тестов;
* запуск существующих тестов;
* AI-review;
* PR/MR;
* merge.

Результат остаётся в локальной ветке.

Система должна явно показать:

```text
CODE READY — NOT TESTED — NOT REVIEWED
```

## 3.5. Реализация UI по скриншоту

Пользователь:

1. создаёт задачу;
2. прикладывает изображение;
3. система определяет изображение как UI reference;
4. coding agent реализует экран;
5. Visual Verification Engine запускает приложение;
6. получает реальный скриншот;
7. сравнивает его с reference;
8. формирует замечания;
9. agent исправляет расхождения;
10. результат остаётся локально или поступает в delivery pipeline.

## 3.6. Генерация дизайна с нуля

Пользователь описывает экран текстом.

Система:

1. изучает дизайн-систему проекта;
2. генерирует варианты изображения;
3. выбирает вариант автоматически или показывает их пользователю;
4. сохраняет выбранный вариант как visual specification;
5. передаёт его coding agent;
6. реализует UI;
7. сверяет реальный результат с visual specification.

---

# 4. Режимы автономности

NeuroForge должен поддерживать готовые профили и пользовательскую конфигурацию стадий.

## 4.1. `PLAN_ONLY`

Разрешено:

* анализировать задачу;
* изучать проект;
* составлять спецификацию;
* строить план;
* оценивать стоимость.

Запрещено:

* изменять код;
* создавать ветку с изменениями;
* выполнять push;
* создавать PR/MR;
* выполнять merge.

## 4.2. `LOCAL_REVIEW`

Рекомендуемый профиль для рабочего проекта.

Разрешено:

* создавать отдельный worktree;
* создавать локальную task branch;
* изменять код;
* создавать локальные checkpoint commits;
* запускать разрешённые проверки;
* формировать итоговый diff.

Запрещено:

* push;
* PR/MR;
* merge;
* публикация артефактов во внешние системы.

## 4.3. `REMOTE_REVIEW`

Разрешено:

* всё из `LOCAL_REVIEW`;
* push task branch;
* создание PR/MR;
* обновление PR/MR.

Запрещено:

* merge.

## 4.4. `AUTONOMOUS`

Разрешено:

* полный pipeline;
* push;
* PR/MR;
* merge;
* post-merge checks;
* auto-revert.

## 4.5. `CUSTOM`

Пользователь управляет каждой стадией отдельно.

---

# 5. Независимые переключатели pipeline

В проекте и в отдельной задаче должны существовать следующие переключатели:

```yaml
pipeline:
  specification:
    enabled: true

  planning:
    enabled: true

  design:
    generate: false
    human_selection: false
    visual_verification: true

  implementation:
    enabled: true

  tests:
    generate: true
    run_existing: true
    run_generated: true
    required_for_completion: true

  review:
    ai_review: true
    security_review: auto
    architecture_review: auto

  git:
    local_checkpoint_commits: true
    final_local_commit: true
    push: false

  change_request:
    create: false
    update_existing: false

  merge:
    enabled: false

  post_merge:
    enabled: false
    auto_revert: false
```

## 5.1. Правила зависимостей

Если `push: false`, система обязана автоматически установить:

```text
change_request.create = false
merge.enabled = false
post_merge.enabled = false
```

Если `change_request.create: false`, но `merge.enabled: true`, конфигурация допустима только для локального merge режима.

Если `tests.generate: false`, агенту запрещено создавать или изменять тестовые файлы, кроме случаев, когда пользователь явно разрешил изменение конкретного файла.

Если `tests.run_existing: false`, система не должна запускать существующие тесты.

Если `visual_verification: false`, UI-задача получает статус:

```text
VISUAL RESULT NOT VERIFIED
```

Если для проекта разрешён автоматический merge UI-задач, Visual Verification Engine должен быть включён, если только project policy явно не разрешает исключение.

## 5.2. Локальные checkpoint commits

Внутренние checkpoint commits разрешены даже в режиме без push.

Они нужны для:

* восстановления после падения;
* переключения между providers;
* сравнения этапов;
* отката неудачного repair;
* хранения результата в task branch.

Checkpoint commit никогда не должен автоматически попадать в пользовательскую основную ветку.

---

# 6. Терминальный интерфейс

## 6.1. Главный экран

```text
NeuroForge                                      18:42

PROJECTS

● PersonalApp      RUNNING       3 active     14 queued
● WorkApp          LOCAL REVIEW  1 active      2 ready
○ Sandbox          PAUSED        0 active      5 queued

ACTIVE RUNS

TASK       PROJECT       STAGE          ENGINE      MODEL
APP-142    PersonalApp   Implementing   Codex       standard
WORK-88    WorkApp       Visual check   Gemini      vision-fast
APP-149    PersonalApp   Reviewing      Claude      small

USAGE TODAY

Coding input       1.4M
Cached input       980K
Coding output      141K
Image generations     6
Estimated cost     $6.81

PROVIDERS

Codex        AVAILABLE
Claude Code  LOW QUOTA
Grok Build   RATE LIMITED
Kimi Code    AVAILABLE
OpenCode     AVAILABLE
Gemini CLI   AVAILABLE
GPT Image    AVAILABLE
Nano Banana  AVAILABLE
```

## 6.2. Основные разделы

TUI должен содержать:

* Projects;
* Tasks;
* Active Runs;
* Providers;
* Models;
* Usage;
* Quotas;
* Designs;
* Settings;
* Audit;
* System Health.

## 6.3. Экран проекта

Показывать:

* путь;
* remote;
* текущую основную ветку;
* target branch;
* factory state;
* autonomy profile;
* разрешённые стадии;
* активные задачи;
* backlog;
* Git status;
* бюджет;
* расход;
* предупреждения;
* установленные project tools.

Действия:

* Start;
* Pause;
* Drain;
* Stop;
* Edit Policy;
* Add Task;
* Open Project;
* Open Shell;
* Run Doctor;
* Show Audit.

## 6.4. Экран задачи

Показывать:

* исходное описание;
* вложения;
* скомпилированную specification;
* assumptions;
* complexity;
* risk;
* execution graph;
* выбранные routes;
* текущего агента;
* токены;
* стоимость;
* изменённые файлы;
* Git diff;
* результаты проверок;
* visual comparison;
* findings;
* историю attempts.

Действия:

* Pause;
* Resume;
* Cancel;
* Send Message;
* Attach File;
* Switch Agent;
* Change Model;
* Retry Stage;
* Skip Optional Stage;
* Open Diff;
* Open Worktree;
* Open in IDE;
* Export Patch;
* Accept Locally;
* Reject Result;
* Push Now;
* Create PR/MR;
* Merge Now.

Права действия определяются project policy.

## 6.5. Интерактивный composer

Внутри задачи должен быть текстовый composer, похожий на coding CLI.

Он поддерживает:

* многострочный текст;
* вставку изображения из clipboard;
* drag-and-drop пути;
* выбор файла;
* историю сообщений;
* упоминание файлов через `@`;
* slash-команды.

Примеры:

```text
@screen.png Реализуй этот экран на Compose.

Используй существующий компонент SubscriptionCard.
Не меняй навигацию.
```

Slash-команды:

```text
/help
/status
/files
/diff
/logs
/pause
/resume
/agent
/model
/budget
/tests
/review
/design
/push
/create-mr
/accept
/reject
```

## 6.6. Command palette

Горячая клавиша открывает fuzzy-search по действиям:

```text
Start project
Pause task
Switch to Codex
Disable test generation
Open current worktree
Generate another design
Compare screenshots
Export patch
```

## 6.7. Изображения в терминале

Если терминал поддерживает вывод изображений, TUI должен отображать preview.

Если не поддерживает:

* показывать имя;
* размер;
* формат;
* путь;
* кнопку `Open externally`.

Система не должна зависеть от поддержки изображений конкретным terminal emulator.

## 6.8. Native provider UI

Основной способ работы — единый TUI NeuroForge.

Дополнительно должна быть escape-hatch команда:

```bash
forge task open-native-agent TASK-ID
```

Она запускает нативный интерактивный интерфейс выбранного coding agent:

* в task worktree;
* с безопасным environment;
* без merge credentials;
* с сохранением task context.

Изменения после выхода должны быть проиндексированы NeuroForge и пройти обычный pipeline.

---

# 7. Установка и bootstrap

## 7.1. Команда `forge init`

После установки бинарного файла пользователь запускает:

```bash
forge init
```

Команда должна открыть onboarding wizard.

## 7.2. Этапы onboarding

### Этап 1. System scan

Проверить:

* ОС;
* архитектуру;
* shell;
* Git;
* package manager;
* права пользователя;
* Docker или Podman;
* GitHub CLI;
* GitLab CLI;
* JDK;
* Android SDK;
* Node.js;
* установленные coding agents;
* доступные image providers.

### Этап 2. Выбор профиля

```text
Minimal
Standard
Android
Web
Full
Custom
```

### Этап 3. Installation plan

Показать:

```text
Будет установлено:

- Git
- Codex CLI
- Claude Code
- Gemini CLI
- Kimi Code
- OpenCode
- Grok Build
- GitHub CLI
- NeuroForge daemon service

Не будет установлено:

- Docker
- Android SDK
- GitLab CLI
```

### Этап 4. Подтверждение

NeuroForge не должен:

* выполнять `sudo` без подтверждения;
* изменять shell profile без показа diff;
* молча ставить глобальные пакеты;
* удалять существующие версии;
* менять авторизацию providers.

### Этап 5. Установка

Установка должна выполняться через platform-specific installers.

Каждый шаг записывается в installation manifest.

### Этап 6. Authentication wizard

Для каждого выбранного provider:

```text
Codex        Login required
Claude Code  Connected
Gemini CLI   Login required
Kimi Code    Connected
Grok Build   Skipped
OpenCode     Configure models
```

Авторизация должна выполняться официальным механизмом provider.

NeuroForge не должен просить пользователя вводить пароль provider непосредственно в NeuroForge.

### Этап 7. Verification

Запустить:

```bash
forge doctor
```

### Этап 8. Создание конфигурации

Создать:

```text
~/.neuroforge/
```

и локальный daemon.

## 7.3. Флаги `init`

```bash
forge init --dry-run
forge init --yes
forge init --profile android
forge init --profile minimal
forge init --no-global
forge init --offline
forge init --repair
forge init --skip-agents
```

## 7.4. Toolchain lock

Сохранить обнаруженные и установленные версии:

```yaml
toolchain:
  git: 2.x
  codex: detected-version
  claude-code: detected-version
  gemini-cli: detected-version
  kimi-code: detected-version
  grok-build: detected-version
  opencode: detected-version
```

Не выполнять автоматическое обновление toolchain во время active task.

## 7.5. Обновление

Команда:

```bash
forge update
```

должна:

1. проверить совместимость;
2. показать план;
3. обновить выбранные компоненты;
4. прогнать adapter conformance tests;
5. при ошибке восстановить предыдущую рабочую конфигурацию.

---

# 8. Управление проектами

## 8.1. Регистрация

```bash
forge project add /path/to/repository
```

TUI должен позволять выбрать директорию через интерактивный picker.

## 8.2. Инициализация проекта

```bash
forge project init PROJECT-ID
```

Создать:

```text
.neuroforge/
├── project.yaml
├── constitution/
│   ├── PRODUCT.md
│   ├── ARCHITECTURE.md
│   ├── ENGINEERING.md
│   ├── AUTONOMY.yaml
│   ├── QUALITY.yaml
│   ├── SECURITY.yaml
│   └── GLOSSARY.md
├── prompts/
├── visual/
│   ├── devices.yaml
│   └── harness.yaml
└── commands.yaml
```

## 8.3. Project onboarding

При первом добавлении система должна:

1. определить языки;
2. определить build system;
3. найти test commands;
4. найти lint;
5. найти contribution rules;
6. найти `AGENTS.md`;
7. найти README;
8. найти CI configuration;
9. определить remote provider;
10. предложить autonomy profile.

Найденные команды сначала показываются пользователю и только после подтверждения записываются в project config.

## 8.4. Project states

```text
DISABLED
STARTING
IDLE
RUNNING
PAUSING
PAUSED
DRAINING
DEGRADED
BLOCKED
ERROR
```

---

# 9. Создание задач

## 9.1. Шаблон необязателен

Пользователь может написать:

```text
На экране оплаты иногда показывается два progress indicator.
Исправь.
```

Task Compiler должен сам:

* найти связанный код;
* сформулировать expected behavior;
* определить scope;
* найти тесты;
* сформировать acceptance criteria;
* выделить assumptions.

## 9.2. Интерактивная форма

Форма содержит:

* Project;
* Title;
* Description;
* Priority;
* Attachments;
* Autonomy profile override;
* Budget override;
* Optional deadline;
* Optional tags.

Обязательны только:

* project;
* непустое описание или вложение.

## 9.3. CLI

```bash
forge task add \
  --project work-app \
  --title "Исправить экран профиля" \
  --attach screenshot.png
```

Коротко:

```bash
forge task add -p work-app "Исправь баг на приложенном скриншоте" -a bug.png
```

## 9.4. Вложения

Поддержать:

* PNG;
* JPEG;
* WebP;
* SVG;
* PDF;
* Markdown;
* plain text;
* JSON;
* YAML;
* логи;
* patch/diff;
* архивы, если разрешено policy.

Каждое вложение получает роль:

```text
DESIGN_REFERENCE
BUG_SCREENSHOT
REQUIREMENTS
LOG
API_SPECIFICATION
EXAMPLE
GENERAL_CONTEXT
```

Роль может определяться автоматически и изменяться пользователем.

## 9.5. Хранение вложений

Вложения сохраняются content-addressed:

```text
~/.neuroforge/artifacts/<hash>
```

Хранить:

* SHA-256;
* исходное имя;
* MIME type;
* размер;
* источник;
* project;
* task;
* confidentiality label.

## 9.6. Политика передачи providers

Для каждого проекта:

```yaml
attachments:
  external_upload:
    images: allow
    source_code: allow
    logs: redact
    documents: ask
```

Перед отправкой:

* удалить известные секреты;
* применить redaction;
* проверить размер;
* записать provider;
* записать purpose.

## 9.7. Уточняющие вопросы

Система не должна блокировать каждую задачу вопросами.

Вопрос задаётся только если:

* безопасное обратимое предположение невозможно;
* варианты ведут к существенно разному продукту;
* требуется действие, запрещённое policy;
* невозможно определить target.

Для остальных случаев Task Compiler выбирает безопасное предположение и фиксирует его.

---

# 10. Архитектура системы

```text
┌─────────────────────────────────────────────┐
│                 Forge TUI                   │
│ Projects · Tasks · Runs · Diff · Settings   │
└──────────────────────┬──────────────────────┘
                       │ Local API + Events
┌──────────────────────▼──────────────────────┐
│                Forge Daemon                 │
│                                             │
│ Project Registry      Task Compiler         │
│ Repo Intelligence     Work Graph Engine     │
│ Scheduler             Model Router          │
│ Quota Manager         Budget Controller     │
│ Workspace Manager     Agent Supervisor      │
│ Design Engine         Visual Verification   │
│ Test Engine           Review Engine         │
│ Merge Governor        Post-Merge Sentinel   │
└───────────┬─────────────────────┬───────────┘
            │                     │
   Coding Agent Adapters   Image Provider Adapters
            │                     │
  Codex · Claude · Grok      GPT Image
  Kimi · OpenCode            Nano Banana
  Gemini CLI                 Plugins
```

---

# 11. Local-first implementation

## 11.1. Один основной binary

```text
forge
```

Он содержит:

* CLI;
* TUI;
* daemon;
* worker process;
* plugin host.

## 11.2. Daemon

Daemon работает локально и:

* хранит state;
* запускает agents;
* отслеживает процессы;
* управляет task queue;
* отправляет события TUI;
* восстанавливается после restart.

## 11.3. Транспорт

Рекомендуемый локальный transport:

* HTTP JSON — команды;
* Server-Sent Events — live events;
* случайный local auth token;
* bind только на loopback.

## 11.4. Durable workflow

Workflow не должен храниться только в RAM.

SQLite должен фиксировать:

* состояние до внешнего действия;
* attempt;
* process ID;
* workspace;
* checkpoint;
* route;
* budget;
* last event.

После restart daemon должен безопасно продолжить или перезапустить attempt.

---

# 12. Coding Agent Engines

Обязательная поддержка:

1. Codex CLI;
2. Claude Code;
3. Grok Build;
4. Kimi Code;
5. OpenCode;
6. Gemini CLI.

Все engines должны использоваться через единый adapter protocol.

## 12.1. Важное разделение

```text
Agent Engine != Model
```

Например:

```yaml
engine: opencode
model: provider/model-name
account: work-api-account
```

## 12.2. Adapter interface

```go
type CodingAgentAdapter interface {
    ID() string

    Detect(context.Context) DetectionResult
    Version(context.Context) VersionResult
    Health(context.Context, Account) HealthResult
    Capabilities(context.Context) AgentCapabilities
    ListModels(context.Context, Account) ([]ModelDescriptor, error)
    InspectQuota(context.Context, Account) QuotaSnapshot

    Start(
        context.Context,
        AgentRunRequest,
        EventSink,
    ) (RunHandle, error)

    Resume(
        context.Context,
        ResumeRequest,
        EventSink,
    ) (RunHandle, error)

    SendMessage(
        context.Context,
        RunHandle,
        AgentMessage,
    ) error

    Cancel(context.Context, RunHandle) error

    ClassifyFailure(
        exitCode int,
        events []NormalizedEvent,
        stderr string,
    ) FailureClassification
}
```

## 12.3. Capabilities

```go
type AgentCapabilities struct {
    InteractiveMode       bool
    HeadlessMode          bool
    StreamingEvents       bool
    StructuredOutput      bool
    ImageInput            bool
    SessionResume         bool
    LiveUserMessages      bool
    ModelSelection        bool
    UsageReporting        bool
    CachedUsageReporting  bool
    ToolPermissions       bool
    NativeSandbox         bool
    MCP                    bool
    ACP                    bool
}
```

## 12.4. Нормализованные события

```text
run.started
run.resumed
message.started
message.delta
message.completed
tool.started
tool.completed
command.started
command.completed
file.changed
usage.updated
checkpoint.created
approval.requested
warning
run.completed
run.failed
run.cancelled
```

---

# 13. Расширяемость coding agents

## 13.1. Declarative adapter

Простой CLI подключается YAML-файлом:

```yaml
api_version: neuroforge/v1
kind: command-coding-agent

id: example-agent

detect:
  command:
    - example-agent
    - --version

run:
  command:
    - example-agent
    - run
    - --cwd
    - "{{ workspace }}"
    - --model
    - "{{ model }}"
    - --output
    - jsonl
    - "{{ prompt_file }}"

capabilities:
  headless_mode: true
  streaming_events: true
  model_selection: true
```

## 13.2. Native plugin

Для сложного adapter:

```text
JSON-RPC 2.0 через stdin/stdout
```

Обязательные методы:

```text
plugin.handshake
agent.detect
agent.health
agent.capabilities
agent.models
agent.quota
run.start
run.resume
run.message
run.cancel
failure.classify
```

## 13.3. Conformance suite

```bash
forge plugin test ./custom-agent
```

Проверить:

* handshake;
* event ordering;
* malformed output;
* cancellation;
* timeout;
* quota failure;
* resume;
* process crash;
* version compatibility.

Добавление нового coding agent не должно требовать изменения:

* scheduler;
* database schema;
* dashboard;
* routing core.

---

# 14. Image Provider API

Image generation должна быть отдельной подсистемой.

Coding agent может:

* подготовить design brief;
* анализировать reference;
* реализовать код;
* оценить результат.

Но генерация изображения должна выполняться через `ImageProviderAdapter`.

## 14.1. Обязательные providers

* OpenAI GPT Image;
* Google Nano Banana.

## 14.2. Интерфейс

```go
type ImageProviderAdapter interface {
    ID() string

    Health(context.Context, Account) HealthResult
    ListModels(context.Context, Account) ([]ImageModel, error)
    InspectQuota(context.Context, Account) QuotaSnapshot

    Generate(
        context.Context,
        ImageGenerationRequest,
        ImageEventSink,
    ) (ImageResult, error)

    Edit(
        context.Context,
        ImageEditRequest,
        ImageEventSink,
    ) (ImageResult, error)

    AnalyzeFailure(error) FailureClassification
}
```

## 14.3. Image model tiers

```text
IMAGE_DRAFT
IMAGE_STANDARD
IMAGE_HIGH_QUALITY
```

Router не должен жёстко привязывать tier к названию модели.

## 14.4. Квоты

Coding quota и image quota учитываются отдельно.

Dashboard должен показывать:

```text
Coding tokens
Image input tokens
Image output tokens
Image generation count
Image cost
```

---

# 15. Design-to-code pipeline

## 15.1. Режимы

```text
OFF
REFERENCE_ONLY
GENERATE_IF_MISSING
ALWAYS_GENERATE
```

## 15.2. Flow с приложенным изображением

```text
Attachment classification
        ↓
Reference analysis
        ↓
Design constraints extraction
        ↓
UI implementation
        ↓
Build and launch
        ↓
Screenshot capture
        ↓
Visual comparison
        ↓
Repair loop
```

## 15.3. Flow без изображения

```text
Task description
        ↓
Project design-system scan
        ↓
Design brief
        ↓
Image generation
        ↓
Variant selection
        ↓
Visual specification locked
        ↓
UI implementation
        ↓
Visual verification
```

## 15.4. Генерация вариантов

Конфигурация:

```yaml
design:
  generation:
    enabled: true
    variants: 3
    model_tier: IMAGE_DRAFT

  selection:
    mode: human
```

`selection.mode`:

```text
HUMAN
AUTOMATIC
FIRST_VALID
```

В `HUMAN` task переходит в состояние:

```text
WAITING_DESIGN_SELECTION
```

Остальные независимые задачи проекта продолжают работу.

## 15.5. Если квота image provider исчерпана

1. открыть circuit breaker;
2. выбрать fallback provider;
3. при отсутствии providers:

   * использовать приложенное изображение, если оно есть;
   * продолжить без генерации, если design generation optional;
   * перевести задачу в `WAITING_QUOTA`, если design обязателен.

## 15.6. Visual specification

Выбранное изображение фиксируется:

```yaml
visual_specification:
  artifact_hash: ...
  viewport:
    width: 1080
    height: 2400

  theme: dark
  locale: ru
  density: xxhdpi
```

После фиксации coding agent не должен произвольно менять дизайн.

---

# 16. Visual Verification Engine

Для UI-задач система должна проверять не только compilation, но и внешний результат.

## 16.1. Visual Harness Adapter

```go
type VisualHarness interface {
    Detect(context.Context, Project) DetectionResult
    Build(context.Context, VisualBuildRequest) error
    Launch(context.Context, LaunchRequest) error
    Navigate(context.Context, NavigationScenario) error
    Capture(context.Context, CaptureRequest) (Screenshot, error)
    Shutdown(context.Context) error
}
```

## 16.2. Обязательная первая реализация

Поддержать command-based generic harness.

Дополнительно реализовать first-class Android harness:

* запуск emulator;
* выбор AVD;
* установка APK;
* запуск Activity;
* настройка locale;
* настройка theme;
* настройка font scale;
* фиксированное разрешение;
* screenshot через Android tooling.

Web harness может быть реализован следующим milestone через browser automation.

## 16.3. Виды проверки

### Детерминированная

* размер изображения;
* viewport;
* наличие пустого экрана;
* clipping;
* overflow;
* contrast checks;
* неожиданные системные ошибки;
* diff regions;
* perceptual similarity.

### Мультимодальная

Vision evaluator проверяет:

* композицию;
* визуальную иерархию;
* соответствие reference;
* отступы;
* размеры;
* цвета;
* typography;
* состояния элементов;
* явные визуальные дефекты.

## 16.4. Результат

```yaml
visual_verification:
  status: failed
  score: 0.78

  issues:
    - severity: major
      region: subscription_card
      description: Card is shorter than reference.

    - severity: minor
      region: title
      description: Font weight differs.

  artifacts:
    reference: ...
    actual: ...
    diff: ...
```

## 16.5. Repair loop

```text
Screenshot
    ↓
Visual findings
    ↓
Targeted UI repair
    ↓
Rebuild
    ↓
New screenshot
```

Конфигурация:

```yaml
visual_verification:
  maximum_iterations: 3
  minimum_score: 0.90
```

## 16.6. Reference-free quality review

Если reference отсутствует, evaluator должен проверить:

* визуальную целостность;
* переполнения;
* читаемость;
* consistency с design system;
* очевидно сломанные состояния.

Он не должен заявлять о pixel-perfect соответствии при отсутствии reference.

---

# 17. Workspace и Git

## 17.1. Основной checkout неприкосновенен

Agents никогда не должны изменять рабочий checkout пользователя.

## 17.2. Worktree

```text
~/.neuroforge/workspaces/
└── project/
    └── task/
        └── work-package/
            └── attempt/
```

## 17.3. Branch naming

```text
forge/<task-id>/<work-package-id>/attempt-<n>
```

Финальная локальная ветка:

```text
forge/result/<task-id>
```

## 17.4. Local review result

После завершения задачи без push TUI показывает:

```text
Result branch:
forge/result/WORK-88

Base:
develop@abc123

Result:
def456

Worktree:
/home/user/.neuroforge/workspaces/...
```

Действия:

* View Diff;
* Open in IDE;
* Open Shell;
* Accept into Current Branch;
* Merge Locally;
* Cherry-pick;
* Export Patch;
* Ask for Changes;
* Reject and Delete;
* Keep for Later;
* Push Manually;
* Create MR Later.

## 17.5. Accept into current branch

Перед применением система должна:

1. проверить пользовательский checkout;
2. отказаться при незакоммиченных конфликтующих изменениях;
3. предложить:

   * merge;
   * squash merge;
   * cherry-pick;
   * apply patch;
4. создать backup reference;
5. выполнить действие;
6. показать результат.

## 17.6. VCS adapters

Поддержать:

* Local Git;
* GitHub Pull Requests;
* GitLab Merge Requests.

Абстракция:

```go
type ChangeRequestProvider interface {
    PushBranch(...)
    CreateChangeRequest(...)
    UpdateChangeRequest(...)
    GetChecks(...)
    EnableAutoMerge(...)
    Merge(...)
    Revert(...)
}
```

Позднее можно добавить другие providers.

---

# 18. Task Compiler и Work Graph

## 18.1. Task Compiler

Превращает свободный ввод в:

* objective;
* acceptance criteria;
* non-goals;
* assumptions;
* constraints;
* risk;
* complexity;
* attachment roles;
* visual requirements;
* proposed scope.

## 18.2. Экономичный каскад

```text
Deterministic parsing
    ↓
Cheap classifier
    ↓
Standard model при низкой уверенности
    ↓
Heavy model только для сложных задач
```

## 18.3. Work Graph

Крупная задача разбивается на DAG:

```text
Research
  ↓
Contract
 ├── Domain
 ├── Storage
 ├── UI
 └── Analytics
       ↓
Integration
       ↓
Verification
```

## 18.4. Semantic leases

Помимо файлов блокируются:

```text
database_schema
navigation_graph
subscription_contract
design_system
build_configuration
```

---

# 19. Model Router

Router выбирает:

```text
Coding engine
+ Model
+ Account
+ Runtime
```

## 19.1. Сигналы

* task role;
* complexity;
* risk;
* language;
* project history;
* model success rate;
* agent engine success rate;
* quota;
* cost;
* latency;
* context size;
* image input;
* structured output;
* provider diversity;
* active load.

## 19.2. Model tiers

```text
TINY
SMALL
STANDARD
HEAVY
FRONTIER
```

Модельные имена находятся в конфигурации и обновляемом catalog.

Core не должен содержать жёстко зашитых текущих model names.

## 19.3. Базовая маршрутизация

```text
C0 → TINY
C1 → SMALL
C2 → STANDARD
C3 → STANDARD / HEAVY
C4 → HEAVY / FRONTIER
```

## 19.4. Escalation

Переход к более сильной модели выполняется, если:

* planner не сформировал уверенный план;
* scope оказался больше прогноза;
* два repair не решили проблему;
* найдена сложная race condition;
* дешёвые agents дали противоречивые результаты;
* требуется архитектурное решение.

## 19.5. De-escalation

После тяжёлого планирования механическую реализацию может выполнять дешёвая модель.

## 19.6. Route explanation

```bash
forge route explain TASK-ID
```

Показать:

* выбранный route;
* альтернативы;
* ожидаемую стоимость;
* quota;
* исторический success rate;
* причины исключения других routes.

---

# 20. Quota Manager

## 20.1. Quota confidence

```text
EXACT
PROVIDER_REPORTED
ESTIMATED
INFERRED
UNKNOWN
```

Dashboard:

```text
125k
~125k
unknown
```

## 20.2. Состояния

```text
AVAILABLE
LOW
EXHAUSTED
RATE_LIMITED
AUTH_REQUIRED
DEGRADED
UNKNOWN
```

## 20.3. Circuit breaker

```text
CLOSED
OPEN
HALF_OPEN
```

При quota exhaustion:

* account блокируется до reset;
* work package получает fallback;
* другие задачи не назначаются на account.

При rate limit:

* использовать retry-after;
* не считать account полностью исчерпанным;
* применять jitter.

При auth failure:

* прекратить automatic retry;
* показать действие `Log in`.

---

# 21. Provider failover

## 21.1. Route chain

```yaml
routes:
  primary:
    engine: codex
    model_tier: STANDARD
    account: codex-main

  fallback:
    - engine: kimi
      model_tier: STANDARD

    - engine: gemini-cli
      model_tier: STANDARD

    - engine: claude-code
      model_tier: HEAVY

    - engine: opencode
      model_tier: SMALL
```

## 21.2. Continuation Pack

При переключении не передавать всю переписку.

```yaml
work_package_id: TASK-123-ui
specification_hash: ...
base_sha: ...
current_sha: ...

completed:
  - project_analyzed
  - screen_implemented

remaining:
  - fix_visual_difference
  - run_final_verification

changes:
  patch: current.patch

failures:
  - type: provider_quota

verification:
  build: passed
  screenshot: failed

next_objective:
  Исправить визуальные расхождения.
```

## 21.3. Checkpoints

Создавать:

* после плана;
* после первого полезного diff;
* после compile;
* после targeted tests;
* после screenshot;
* перед quota switch;
* перед repair;
* перед integration.

---

# 22. Token Optimization Engine

## 22.1. Запрет полного repo dump

Нельзя автоматически помещать весь репозиторий в prompt.

## 22.2. Repo index

Использовать:

* Git;
* file tree;
* symbol index;
* imports;
* build graph;
* test graph;
* SQLite FTS;
* history of related changes.

Vector database не обязательна для первой версии.

## 22.3. Context Pack

Агент получает:

* specification;
* allowed scope;
* repo map;
* релевантные файлы;
* архитектурные правила;
* команды;
* последние failures;
* ссылки на полные artifacts.

## 22.4. Log slicing

Модели не передаются полные огромные логи.

Передавать:

* exit code;
* failing command;
* первую ошибку;
* релевантный stack trace;
* summary остальных ошибок;
* путь к полному логу.

## 22.5. Delta repair context

Repair agent получает:

* finding;
* текущий diff;
* failing test;
* необходимые файлы.

Не получает повторно всю историю исследования.

## 22.6. Детерминированные операции

Запрещено использовать LLM для:

* Git status;
* Git diff;
* worktree;
* merge;
* JSON validation;
* schema validation;
* запуска команд;
* подсчёта файлов;
* quota arithmetic;
* budget arithmetic;
* проверки scope;
* удаления workspace.

## 22.7. Turn limits

```yaml
turn_limits:
  C0: 4
  C1: 8
  C2: 16
  C3: 28
  C4: 40
```

Перед достижением лимита agent создаёт checkpoint.

## 22.8. Prompt cache

Повторяющиеся project instructions должны иметь стабильный порядок и fingerprint, чтобы providers, поддерживающие caching, могли переиспользовать context.

## 22.9. Project Memory

Хранить только структурированные знания:

* architecture facts;
* build commands;
* design-system rules;
* known failures;
* accepted decisions;
* provider quirks.

Запись содержит:

* source;
* confidence;
* scope;
* commit SHA;
* expiration policy.

---

# 23. Budget Controller

Поддержать:

* global budget;
* daily budget;
* monthly budget;
* project budget;
* task budget;
* coding provider budget;
* image provider budget.

```yaml
budgets:
  global:
    daily_usd: 20
    monthly_usd: 300

  project:
    work-app:
      daily_usd: 5

  task_defaults:
    R0: 0.20
    R1: 1.00
    R2: 4.00
    R3: 12.00
    R4: 30.00

  image:
    daily_usd: 3
    maximum_variants_per_task: 4
```

Soft limit:

* выбрать более дешёвый route;
* уменьшить число design variants;
* отложить optional review.

Hard limit:

* запретить новые платные runs;
* разрешить только включённые в подписку routes, если policy это допускает;
* поставить задачу в `BUDGET_EXCEEDED`.

---

# 24. Тестирование

## 24.1. Независимые настройки

```yaml
tests:
  generate: false
  modify_existing: false
  run_existing: true
  run_generated: false
  require_for_local_result: false
  require_for_remote_merge: true
```

## 24.2. Если генерация тестов отключена

Agent prompt явно содержит:

```text
Do not create or modify tests.
```

Scope Validator отклоняет изменение test files.

## 24.3. Progressive verification

```text
Syntax
  ↓
Compile changed module
  ↓
Targeted tests
  ↓
Module tests
  ↓
Full verification
```

Не запускать полный pipeline после каждого маленького edit.

## 24.4. Локальный результат без тестов

Допускается в `LOCAL_REVIEW`.

Финальный статус:

```text
IMPLEMENTED
NOT TESTED
NOT REVIEWED
LOCAL BRANCH ONLY
```

## 24.5. Автоматический merge

Merge Governor применяет project policy.

Отключение тестов в task override не должно автоматически обходить обязательные merge rules.

---

# 25. AI-review

Настройки:

```yaml
review:
  enabled: false

  roles:
    correctness: true
    architecture: auto
    security: auto
    visual: auto

  independent_provider: preferred
```

## 25.1. Review выключен

Допускается для local result.

Результат помечается:

```text
NOT AI-REVIEWED
```

## 25.2. Review для merge

Project policy может требовать review для определённого risk.

Task override не может ослабить неотключаемую security policy проекта.

---

# 26. Risk Engine

```text
R0 — документация и механические изменения
R1 — локальный UI, analytics, простая логика
R2 — public API, provider integration, background jobs
R3 — migrations, concurrency, subscriptions
R4 — auth, payments, permissions, destructive changes
```

Risk влияет на:

* model tier;
* reviews;
* tests;
* visual validation;
* runtime isolation;
* delivery permissions;
* rollback;
* бюджет.

---

# 27. Verification Evidence

Каждый acceptance criterion связывается с evidence.

```yaml
acceptance_evidence:
  AC-1:
    type: test
    reference: SubscriptionTest.secondTrialIsGranted

  AC-2:
    type: visual
    reference: visual-report.json

  AC-3:
    type: static
    reference: scope-check
```

При отключённых тестах допустимы другие evidence, но система должна снижать confidence.

---

# 28. Merge Governor

Merge Governor — детерминированный код.

Он проверяет:

```yaml
merge:
  specification_locked: true
  scope_valid: true
  required_checks_passed: true
  acceptance_evidence_complete: true
  blocker_findings: 0
  major_findings: 0
  target_allowed: true
  branch_current: true
  budget_policy_satisfied: true
  visual_policy_satisfied: true
```

Решения:

```text
ALLOW_LOCAL_RESULT
ALLOW_PUSH
ALLOW_CHANGE_REQUEST
ALLOW_MERGE
REQUIRE_REBASE
REQUIRE_REPAIR
POLICY_BLOCKED
QUARANTINE
```

Agent process не имеет merge credentials.

---

# 29. Security

## 29.1. Рабочий safe profile

```yaml
security:
  network: restricted
  push: denied
  change_request: denied
  merge: denied
  external_artifact_upload: restricted
  production_secrets: denied
  destructive_commands: ask
```

## 29.2. Environment

Не передавать дочерним процессам весь environment.

Использовать allowlist.

Agent не должен получать:

* VCS merge token;
* production credentials;
* unrelated API keys;
* daemon auth token.

## 29.3. Prompt injection priority

```text
Factory Security Policy
  >
Project Constitution
  >
Task Specification
  >
Repository Documentation
  >
Source Comments
  >
External Attachments
```

Инструкция внутри README не может включить push или отключить security.

## 29.4. Audit

Записывать:

* команды;
* изменённые файлы;
* provider;
* model;
* передачу вложений;
* push;
* PR/MR;
* merge;
* revert;
* policy override.

---

# 30. Основные CLI-команды

```bash
forge
forge init
forge doctor
forge dashboard

forge project add
forge project init
forge project list
forge project start
forge project pause
forge project drain
forge project stop
forge project settings

forge task add
forge task list
forge task show
forge task pause
forge task resume
forge task cancel
forge task retry
forge task diff
forge task open
forge task accept
forge task reject
forge task push
forge task create-pr
forge task create-mr

forge agent list
forge agent doctor
forge agent enable
forge agent disable
forge agent runs

forge model list
forge model refresh
forge route explain

forge image-provider list
forge image-provider doctor

forge quota
forge usage
forge cost
forge cost forecast

forge plugin list
forge plugin install
forge plugin test

forge audit
forge emergency-stop
forge cleanup
forge update
```

Команды чтения поддерживают:

```text
--json
```

---

# 31. Хранилище

SQLite в WAL-режиме.

Минимальные таблицы:

```text
projects
project_policies
tasks
task_attachments
task_specifications
work_packages
dependencies
semantic_leases
attempts
agent_runs
run_events
checkpoints
continuation_packs
coding_engines
image_providers
accounts
models
quota_snapshots
circuits
route_decisions
usage_events
verification_runs
visual_runs
visual_findings
review_findings
merge_candidates
change_requests
post_merge_checks
artifacts
audit_events
schema_migrations
```

Большие artifacts хранятся в файловой системе, а не BLOB.

---

# 32. Отказоустойчивость

Классы ошибок:

```text
PROVIDER_QUOTA
PROVIDER_RATE_LIMIT
PROVIDER_CAPACITY
PROVIDER_AUTH
ENGINE_NOT_INSTALLED
ENGINE_CRASH
ENGINE_PROTOCOL_ERROR
MODEL_NOT_AVAILABLE
IMAGE_PROVIDER_FAILURE
TIMEOUT
CANCELLED
BUILD_FAILURE
TEST_FAILURE
VISUAL_FAILURE
SCOPE_VIOLATION
POLICY_VIOLATION
MALFORMED_OUTPUT
MERGE_CONFLICT
BUDGET_EXCEEDED
INTERNAL_ERROR
```

Каждый класс имеет policy:

* retry;
* cooldown;
* failover;
* escalation;
* pause;
* quarantine.

Бесконечные retry запрещены.

---

# 33. Тестирование самого NeuroForge

## 33.1. Fake coding agent

Реализовать сценарии:

* success;
* quota before edits;
* quota after edits;
* rate limit;
* malformed JSON;
* timeout;
* crash;
* scope violation;
* successful resume.

## 33.2. Fake image provider

Сценарии:

* generation success;
* generation quota;
* invalid image;
* timeout;
* failover;
* deterministic fixture generation.

## 33.3. Fake visual harness

Создаёт:

* matching screenshot;
* mismatch;
* blank screen;
* clipped UI;
* startup failure.

## 33.4. Integration scenarios

* local-only task;
* no-test task;
* no-review task;
* push-disabled task;
* GitHub PR task;
* GitLab MR task;
* coding quota failover;
* image quota failover;
* daemon restart;
* visual repair;
* local accept;
* task rejection;
* auto-merge;
* auto-revert.

Реальные providers не используются в обычном CI.

---

# 34. Milestones

## M0 — Foundation

* Go repository;
* SQLite;
* migrations;
* daemon;
* event log;
* CLI skeleton;
* TUI shell.

## M1 — Projects and local tasks

* project registry;
* project init;
* local backlog;
* project/task TUI;
* project states.

## M2 — Agent protocol

* common interfaces;
* normalized events;
* command adapter;
* plugin protocol;
* fake coding agent.

## M3 — Workspaces

* Git worktree;
* branches;
* leases;
* process supervision;
* checkpoint;
* local review result.

## M4 — Initial coding engines

* Codex;
* Claude Code;
* Gemini CLI.

## M5 — Remaining coding engines

* Kimi Code;
* Grok Build;
* OpenCode.

Adapters M4 и M5 могут реализовываться параллельно после стабилизации protocol.

## M6 — Routing, quota and budget

* model catalog;
* complexity;
* risk;
* route selection;
* circuit breakers;
* usage;
* dashboard.

## M7 — Failover

* continuation packs;
* provider switching;
* recovery;
* session policy.

## M8 — Configurable tests and review

* stage toggles;
* verification;
* findings;
* repair loops.

## M9 — Image providers

* GPT Image adapter;
* Nano Banana adapter;
* fake image provider;
* image budgets.

## M10 — Design and visual verification

* design flow;
* visual specification;
* generic visual harness;
* Android harness;
* screenshot comparison;
* visual repair.

## M11 — Remote delivery

* GitHub Pull Requests;
* GitLab Merge Requests;
* push policy;
* Merge Governor.

## M12 — Post-merge and optimization

* post-merge sentinel;
* auto-revert;
* repo index;
* context optimization;
* quality statistics.

## M13 — Bootstrap

* `forge init`;
* installation planner;
* profiles;
* toolchain lock;
* update and repair.

`forge init` может разрабатываться раньше, но считается завершённым после появления всех adapters.

---

# 35. Критерии приёмки

## AC-1

`forge` без аргументов открывает интерактивный TUI.

## AC-2

Пользователь управляет проектами и задачами без ввода CLI-команд.

## AC-3

Можно создать задачу свободным текстом без шаблона.

## AC-4

К задаче можно прикрепить изображение.

## AC-5

Поддерживаются:

* Codex;
* Claude Code;
* Grok Build;
* Kimi Code;
* OpenCode;
* Gemini CLI.

## AC-6

Тестовый седьмой agent подключается plugin-ом без изменения core.

## AC-7

В `LOCAL_REVIEW` ни один Git network operation не выполняется.

## AC-8

Код сохраняется в отдельной локальной result branch.

## AC-9

Пользователь может открыть diff и worktree из TUI.

## AC-10

Пользователь может принять, отклонить или попросить доработку.

## AC-11

Генерацию тестов можно отключить.

## AC-12

Запуск существующих тестов можно отключить отдельно.

## AC-13

AI-review можно отключить.

## AC-14

Push, PR/MR и merge переключаются отдельно.

## AC-15

Fake agent quota failure после изменения файлов приводит к continuation через fallback без потери checkpoint.

## AC-16

Простая задача получает дешёвый route.

## AC-17

Сложная задача эскалируется к сильной модели.

## AC-18

Dashboard показывает exact и estimated usage с разными обозначениями.

## AC-19

Поддерживаются GPT Image и Nano Banana adapters.

## AC-20

Из текстового задания можно сгенерировать visual specification.

## AC-21

Из приложенного изображения можно создать UI implementation task.

## AC-22

Visual Verification Engine получает реальный screenshot.

## AC-23

При визуальном расхождении запускается repair loop.

## AC-24

При отключённой visual verification система не утверждает, что UI проверен.

## AC-25

`forge init --dry-run` показывает план и не изменяет систему.

## AC-26

`forge init` устанавливает выбранные tools, предлагает официальную авторизацию и запускает doctor.

## AC-27

Daemon восстанавливает незавершённые задачи после restart.

## AC-28

Agent не имеет merge credentials.

## AC-29

Стадии, обязательные project security policy, нельзя отключить task override.

## AC-30

Полная история задачи доступна в audit:

```text
input
→ specification
→ route
→ attempts
→ usage
→ changes
→ verification
→ delivery
```

---

# 36. Правила реализации

1. Не реализовывать все компоненты одним огромным package.
2. Не превращать продукт в набор микросервисов.
3. Не добавлять Kubernetes.
4. Не добавлять web UI до завершения TUI.
5. Не использовать реальные платные models в обычных тестах.
6. Сначала создать fake coding agent.
7. Сначала стабилизировать adapter protocol, затем параллельно писать adapters.
8. Не привязывать core к текущим названиям моделей.
9. Не смешивать coding agent и image provider.
10. Не считать CLI subscription quota точной, если provider не предоставляет точное значение.
11. Не передавать весь repository в prompt.
12. Не использовать LLM для Git и policy enforcement.
13. Не выполнять push в `LOCAL_REVIEW`.
14. Не модифицировать основной checkout пользователя.
15. Не разрешать coding agent менять project security policy.
16. Не позволять агенту отключать проверки, проверяющие его результат.
17. Не выполнять silent installation.
18. Не выполнять silent privilege escalation.
19. Не обновлять provider CLI во время active run.
20. После каждого milestone приложение должно собираться и иметь демонстрируемый сценарий.
21. Каждое архитектурное отклонение фиксировать ADR.
22. Каждое требование AC должно иметь автоматический или интеграционный тест.
23. Спецификация является источником истины.
24. Агент не имеет права самостоятельно упрощать scope проекта.
25. Нереализованные требования должны быть явно отмечены, а не замаскированы stub-ами.

---

# 37. Definition of Done

Пользователь устанавливает NeuroForge и выполняет:

```bash
forge init
forge
```

В TUI он:

1. добавляет личный и рабочий проекты;
2. для личного выбирает `AUTONOMOUS`;
3. для рабочего выбирает `LOCAL_REVIEW`;
4. отключает генерацию тестов для конкретной задачи;
5. прикладывает screenshot;
6. запускает задачу;
7. наблюдает за route, токенами и процессом;
8. видит visual comparison;
9. получает код в локальной branch;
10. открывает diff;
11. принимает или отклоняет изменение;
12. убеждается, что push и MR не выполнялись.

На личном проекте фабрика должна иметь возможность пройти полный путь:

```text
Task
→ Specification
→ Design
→ Implementation
→ Visual Verification
→ Tests
→ Review
→ PR
→ Merge
→ Post-Merge Check
```

На рабочем проекте:

```text
Task
→ Implementation
→ Optional Checks
→ Local Branch
→ Human Review
```

Главный продуктовый результат:

```text
NeuroForge позволяет использовать автономных AI-агентов
как на личных проектах, так и в безопасном режиме
на профессиональной кодовой базе.
```
