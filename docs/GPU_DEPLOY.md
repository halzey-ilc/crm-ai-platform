# Запуск на GPU-сервере

Как поднять AI Platform на сервере с видеокартой NVIDIA: текстовая модель, распознавание речи
(русский, английский и отдельно кыргызский) и поиск по документам.

## 1. Подготовка сервера (один раз)

1. Драйвер NVIDIA: `nvidia-smi` должен показать видеокарту и объём памяти.
2. Docker и NVIDIA Container Toolkit. Проверка:
   ```bash
   docker run --rm --gpus all nvidia/cuda:12.6.3-base-ubuntu24.04 nvidia-smi
   ```
3. Код и секреты:
   ```bash
   git clone https://github.com/halzey-ilc/crm-ai-platform && cd crm-ai-platform
   make env        # создаёт .env со случайными секретами; файл не коммитится
   ```

## 2. Какая модель

Модель выбирается по памяти видеокарты (`nvidia-smi`, колонка Memory). Ориентир для GGUF
Q4_K_M: около 0,6 ГБ на миллиард параметров, плюс 2–6 ГБ на контекст для нескольких
одновременных запросов, плюс ~2 ГБ на Whisper и ~1 ГБ на кыргызскую модель речи.

| Память GPU | Текстовая модель | Одновременных запросов (`AI_LLM_PARALLEL`) |
|---|---|---|
| 16 ГБ | до ~8B | 2–4 |
| 24 ГБ | до ~14B | 4 |
| 48 ГБ | до ~32B | 4–8 |
| 80 ГБ | до ~70B или 32B с запасом | 8 |

Модель задаётся в `.env` и скачивается скриптом с проверкой sha256:

```bash
AI_MODEL_REPO=<репозиторий GGUF на Hugging Face> AI_MODEL_FILE=<файл .gguf> ./scripts/get_model.sh
echo "AI_LLM_MODEL_FILE=<файл .gguf>" >> .env
echo "AI_LLAMACPP_MODEL=<короткое имя для отчётов>" >> .env
```

После смены модели обязательно: `make eval-baseline` (эталон качества), затем сравнение
`make eval-llm` при каждом обновлении.

## 3. Кыргызская речь

Whisper не знает кыргызского. Нужна модель, дообученная на кыргызском, в формате whisper.cpp:

```bash
./scripts/get_ky_whisper.sh                                         # по умолчанию AkylAI-STT-small
AI_KY_WHISPER_REPO=BuzzASR/kyrgyz ./scripts/get_ky_whisper.sh       # крупнее, проверить лицензию
```

Выбор между моделями — только по своим записям: 10–20 кыргызских звонков (с согласия) с ручной
расшифровкой, сравнить долю ошибок по символам. Пока это не сделано, кыргызские расшифровки
помечаются в CRM как черновые.

## 4. Запуск

```bash
make up-gpu
docker compose -f deployments/local/compose.yaml ps   # все сервисы healthy
nvidia-smi                                            # модели заняли память видеокарты
```

Проверка: `make playground` — страница ручной проверки всех типов задач, включая оценку звонка
(`call.review.v1`).

## 5. Связь с CRM

В `.env` CRM: `AI_PLATFORM_ENABLED=true`, `AI_PLATFORM_URL=<адрес шлюза>`,
`AI_PLATFORM_TOKEN_KEY=<тот же ключ, что AI_JWT_KEY здесь>`. Шлюз не публиковать в интернет
напрямую: только внутренняя сеть или VPN между CRM и этим сервером.
