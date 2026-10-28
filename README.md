# CRM AI Platform

Отдельный AI Backend для CRM-экосистемы. CRM Backend (`../crm-platform`, Django)
остаётся источником истины для бизнес-данных и правил; AI Platform выполняет
AI-задачи и возвращает проверенные структурированные результаты.

> **Статус: Foundation.** Работают gateway, система задач, orchestrator, mock
> provider, контракты, изоляция компаний (RLS), Docker, тесты, Training Toolkit
> на синтетических данных. **Ни одна реальная модель не обучена и не подключена**:
> реальных данных нет ([аудит](docs/DATA_READINESS_AUDIT.md)), а интеграция с CRM
> требует доработок CRM ([интеграция](docs/CRM_INTEGRATION.md)). Не production-ready.

## Быстрый старт

```bash
make env            # .env со случайными секретами
make up             # PostgreSQL (127.0.0.1:55432), migrate, gateway (127.0.0.1:8090), orchestrator
./scripts/smoke.sh  # задача → результат → проверка изоляции
make check          # lint + unit-тесты Go + тесты Toolkit (без сервисов)
make test-integration
```

Нужны: Go 1.27, Docker + Compose, Make, uv (для Toolkit). GPU и платные API не нужны.

## Структура

```
contracts/            JSON Schema входов и выходов (версионированы), Go-загрузчик
services/             бинарники: gateway, orchestrator, migrate
internal/             auth, config, jobs (домен), jobtypes, modelgw (+mock), orchestrator,
                      httpapi, store/postgres (миграции, RLS), platform
tools/devtoken        выпуск dev-токенов (только не-production)
tests/integration     тесты на реальном PostgreSQL, включая межтенантные
training/toolkit      Python Training Toolkit (uv)
deployments/          Dockerfile, локальный compose
docs/                 архитектура, интеграция, контракты, безопасность, данные, ADR
```

## Документация

- [Архитектура](docs/AI_PLATFORM_ARCHITECTURE.md) · [Roadmap](docs/AI_ROADMAP.md) · [Статусы](docs/STATUS_LEGEND.md)
- [Интеграция с CRM](docs/CRM_INTEGRATION.md) · [Контракты API](docs/API_CONTRACTS.md)
- [Безопасность](docs/AI_SECURITY.md) · [Управление данными](docs/AI_DATA_GOVERNANCE.md)
- [Аудит готовности данных](docs/DATA_READINESS_AUDIT.md)
- [Стратегия обучения](docs/TRAINING_STRATEGY.md) · [Оценка моделей](docs/MODEL_EVALUATION.md)
- [Эксплуатация](docs/OPERATIONS.md) · [Решения (ADR)](docs/DECISIONS/)
