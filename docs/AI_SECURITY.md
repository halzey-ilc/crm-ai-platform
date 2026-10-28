# Безопасность AI Platform

Статусы — [STATUS_LEGEND.md](STATUS_LEGEND.md). Проверки выполняются локально и в
GitHub Actions (`.github/workflows/ci.yml`). Первый запуск CI нашёл через
govulncheck уязвимости GO-2026-6629 и GO-2026-5970 в `golang.org/x/text` v0.29.0
(косвенная зависимость pgx); исправлено обновлением до v0.43.0, после чего CI зелёный.

## 1. Изоляция компаний (defense in depth)

| Слой | Механизм | Статус и доказательство |
|---|---|---|
| Идентичность | Tenant только из подписанного токена; tenant в теле отклоняется; `X-Tenant` ≠ токену → 403 | ✅ `TestTenantInBodyIsRejected`, `TestCrossTenantAccessLooksLikeNotFound` |
| API | Чужая задача неотличима от несуществующей (404) — компании нельзя перечислить | ✅ те же тесты + `TestStoreHidesOtherTenantsJobs` |
| Код | Каждый запрос к БД идёт с `WHERE tenant_id = $n` | ✅ |
| **База данных** | Row-Level Security: роль `ai_app` видит и пишет только строки с `tenant_id = app.tenant_id` (транзакционная настройка). Без настройки строк не видно | ✅ `TestRowLevelSecurityWithoutAppFilters` — проверено **без** фильтров в SQL |
| Подмена tenant при записи | `WITH CHECK` отклоняет INSERT/UPDATE чужого tenant и перенос строки | ✅ тот же тест |
| Результаты | Отдельная RLS-политика на `ai_job_result` | ✅ `TestResultsAreTenantIsolated` |
| Идемпотентность | Ключи уникальны в пределах tenant — чужой ключ не возвращает чужую задачу | ✅ |
| Воркер | Межтенантное чтение есть ровно в одном месте: `SECURITY DEFINER ai_claim_jobs`. Дальнейшая работа идёт в транзакции под tenant задачи | ✅ |
| Кэш | Кэша нет. Когда появится, ключ обязательно начинается с tenant | 📐 |
| Поиск / RAG | Фильтрация по tenant и правам **до** поиска | 📐 этап H (Knowledge Assistant) |
| Внешние модели | Модель получает только вход задачи, созданной по токену этого tenant | ✅ (mock); 📐 политика для внешних провайдеров |

Сервисная идентичность AI **не** даёт доступа ко всем компаниям: каждый токен
привязан к одному tenant.

## 2. Привилегии БД

Роль `ai_app`: `LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE`,
права только `SELECT/INSERT/UPDATE` на `ai_job`, `SELECT/INSERT` на
`ai_job_result`. **DELETE нет**: удаление — отдельная процедура
([AI_DATA_GOVERNANCE.md](AI_DATA_GOVERNANCE.md)). DDL недоступен. ✅
`TestAppRoleHasNoDangerousPrivileges`.

Миграции выполняет владелец схемы (`AI_DATABASE_OWNER_URL`), отдельным процессом `migrate`.

## 3. Аутентификация сервиса

- HS256 через golang-jwt, разрешён только алгоритм HS256 (`alg: none` и подмена payload
  отклоняются). ✅ `TestRejectedTokens`
- Обязательны `iss`, `aud`, `exp` и `iat`. Срок жизни ограничен (по умолчанию 10 мин,
  максимум 1 ч). ✅
- Ротация ключа без простоя через `AI_AUTH_HS256_KEY_PREVIOUS`. ✅
  `TestPreviousKeyAcceptedDuringRotation`
- Ключ ≥ 32 байт, плейсхолдер `__GENERATE__` запрещён. ✅
- Выпуск токенов в CRM: 📐 зависит от изменения CRM ([CRM_INTEGRATION.md §3.1](CRM_INTEGRATION.md)).

## 4. Секреты

- `.env` в `.gitignore` и `.dockerignore`. `scripts/init_env.sh` генерирует значения с правами
  600 и не выводит их. ✅
- Любой секрет можно передать файлом (`<NAME>_FILE`) — для Docker secrets или Vault. ✅
- Ошибки конфигурации называют переменную, но не значение. ✅ `TestErrorsNeverContainSecretValues`
- Секретов в репозитории нет. Проверено вручную; 📐 автоматический тест-сканер.

## 5. Логи и данные

- Логи — JSON (slog). **Входы и выходы моделей не логируются**: в логах только job_id,
  tenant, тип задачи, коды ошибок и длительности. ✅ по коду
- Ошибки валидации не содержат значений полей. ✅ `TestValidationErrorNeverEchoesValues`
- API не возвращает вход задачи. ✅
- Сообщение об ошибке задачи — фиксированный текст по коду, без деталей провайдера. ✅

## 6. Production-ограничения в конфигурации

При `AI_ENV=production` процесс не стартует, если:
- включён mock provider;
- `AI_DATABASE_URL` без `sslmode=verify-full`.

✅ `TestGatewayRejectsUnsafeSettings`

## 7. Контейнеры

Образы distroless/static, non-root, без shell. В compose: `read_only`,
`cap_drop: ALL`, `no-new-privileges`. Порты слушают только 127.0.0.1. ✅
(образы собраны, стек запущен локально)

## 8. Не сделано (известные пробелы)

- ✅ Сканирование Go-зависимостей в CI (`govulncheck`). 📐 Сканирование образов (Trivy) и
  Python-зависимостей (pip-audit).
- 📐 TLS на gateway: предполагается TLS-терминация на edge, как в CRM (nginx).
- 📐 Аудит чувствительных операций в отдельный журнал. Сейчас есть только access log и
  структурированные логи воркера.
- 📐 Rate limit общий для нескольких реплик gateway (сейчас лимит действует на одну реплику).
- 📐 Политика для внешних LLM: что можно отправлять, обезличивание, договор с поставщиком.
  До её согласования внешние провайдеры не подключаются.
- 📐 AI Agent Runtime: белый список инструментов, подтверждение рискованных действий,
  никакого SQL и shell.
