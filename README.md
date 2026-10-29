# CRM AI Platform

Отдельный AI Backend для CRM-экосистемы. CRM Backend (`../crm-platform`, Django)
остаётся источником истины для бизнес-данных и правил; AI Platform выполняет
AI-задачи и возвращает проверенные структурированные результаты.

> **Статус: первые версии всех 8 AI-направлений; не production-ready.**
>
> - **Работает:** gateway, очередь задач, orchestrator, изоляция компаний (RLS), Model Gateway
>   с провайдерами `mock` и `llamacpp`, метрики Prometheus, очистка по сроку хранения.
> - **Типы задач:** 10 (с версиями), у каждого проверка «без выдумок».
> - **CRM:** интеграция работает. CRM сама присылает данные и забирает результат, по
>   умолчанию AI в CRM выключен.
> - **Локальная модель:** Qwen3.5-4B на llama.cpp ([ADR-006](docs/DECISIONS/ADR-006-local-llm-llamacpp.md)).
>   На настоящей модели проверено только резюме звонков, остальное — на `mock`.
>   Для рабочей скорости нужен GPU.
> - **Своей обученной модели нет:** нет реальных данных
>   ([аудит](docs/DATA_READINESS_AUDIT.md)). Toolkit и реестр моделей работают на синтетике.
> - Что ждёт железа, данных или решений — таблица «Отложено» в [roadmap](docs/AI_ROADMAP.md).

## Типы задач

| Тип | Направление | Сквозная проверка через CRM |
|---|---|---|
| `call.summary.v2` (и v1) | Call Intelligence | `scripts/e2e_with_crm.sh` |
| `operations.digest.v1` | Operations | `scripts/e2e_operations.sh` |
| `kpi.explanation.v1` | Business Analyst | `scripts/e2e_kpi.sh` |
| `message.reply.v1` | Communication Assistant | `scripts/e2e_reply.sh` |
| `deal.coach.v1` | Sales Coach | `scripts/e2e_coach.sh` |
| `risk.review.v1` | Risk & Anomaly | `scripts/e2e_risk.sh` |
| `agent.plan.v1` | Agent Runtime | `scripts/e2e_agent.sh` |
| `agent.plan.v2` (встреча, этап сделки) | Agent Runtime | только unit-тесты (CRM не подключена) |
| `knowledge.answer.v1` + `/v1/knowledge/*` | Knowledge Assistant | `scripts/smoke_knowledge.sh` (без CRM) |

Контракты и правила проверки — [API_CONTRACTS.md](docs/API_CONTRACTS.md).
Скрипты `e2e_*` используют локальную CRM (`../crm-platform`). `AI_USE_LLM=0` запускает их на
mock, `AI_USE_LLM=1` — на локальной модели.

## Быстрый старт

```bash
make env            # .env со случайными секретами
make up             # PostgreSQL (127.0.0.1:55432), migrate, gateway (127.0.0.1:8090), orchestrator
./scripts/smoke.sh  # задача → результат → проверка изоляции
make check          # lint + unit-тесты Go + тесты Toolkit + Rust (без сервисов)
make test-integration
make up-llm         # то же с локальной моделью (скачает её при первом запуске, 3 ГБ, sha256)
```

Нужны: Go 1.27, Docker + Compose, Make, uv (для Toolkit), Rust (для `runtimes/redact`).
Платные API не нужны. GPU не обязателен: без него локальная модель работает, но медленно.

Метрики: `127.0.0.1:9464/metrics` (gateway) и `127.0.0.1:8091/metrics` (orchestrator).

## Структура

```
contracts/            JSON Schema входов и выходов (версионированы), Go-загрузчик
services/             бинарники: gateway, orchestrator, migrate
internal/             auth, config, jobs (домен), jobtypes, modelgw (mock, llamacpp),
                      orchestrator (+ очистка), httpapi, store/postgres (миграции, RLS),
                      metrics, platform; правила проверки: callsummary, kpinum,
                      replycheck, dealcoach, riskreview, agentplan
tools/devtoken        выпуск dev-токенов (только не-production)
tests/integration     тесты на реальном PostgreSQL, включая межтенантные
training/toolkit      Python Training Toolkit (uv) и реестр моделей (`crmai-train registry`)
runtimes/redact       обезличивание текста (Rust)
evaluation/           синтетический набор и отчёт оценки резюме звонков
scripts/              smoke, e2e через CRM, загрузка модели
deployments/          Dockerfile, локальный compose (+ compose.llm.yaml)
docs/                 архитектура, интеграция, контракты, безопасность, данные, ADR
```

## Документация

- [Архитектура](docs/AI_PLATFORM_ARCHITECTURE.md) · [Roadmap](docs/AI_ROADMAP.md) · [Статусы](docs/STATUS_LEGEND.md)
- [Интеграция с CRM](docs/CRM_INTEGRATION.md) · [Контракты API](docs/API_CONTRACTS.md)
- [Безопасность](docs/AI_SECURITY.md) · [Управление данными](docs/AI_DATA_GOVERNANCE.md)
- [Аудит готовности данных](docs/DATA_READINESS_AUDIT.md)
- [Стратегия обучения](docs/TRAINING_STRATEGY.md) · [Оценка моделей](docs/MODEL_EVALUATION.md)
- [Эксплуатация](docs/OPERATIONS.md) · [Решения (ADR)](docs/DECISIONS/)
