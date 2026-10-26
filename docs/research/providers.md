# framy: механика API OpenAI, Gemini API и OpenRouter

Дата исследования: 2026-10-26. Списки моделей здесь не собираются (их делают другие агенты), только механика, лимиты, цены как формулы, проверка ключей, OAuth, валюта, Stars.

Пометки к фактам:
- [OK] проверено по официальной документации, URL рядом
- [CURL] проверено запросом к публичному эндпоинту
- [ФОРУМ] только неофициальные источники
- [НЕ ПРОВЕРЕНО] не удалось проверить, это вывод или предположение

Реальные ключи пользователей и свои платные ключи не использовались. Поведение платных эндпоинтов описано по документации.

---

## 0. Главные находки (меняют дизайн)

1. У OpenAI видео больше нет. Videos API и Sora 2 (`sora-2`, `sora-2-pro` и снапшоты) закрыты 2026-09-24, замены нет [OK] https://developers.openai.com/api/docs/deprecations, https://developers.openai.com/api/docs/guides/video-generation. Провайдер OpenAI в framy только для картинок.
2. У Gemini API закрыты Imagen (shutdown 2026-08-17) и Veo 3.1 preview (2026-10-22); Veo 2 и 3.0 закрыты 2026-06-30. Veo GA живёт только на отдельной платформе с GCP-аутентификацией, с вставленным API-ключом AI Studio недоступен [OK] страницы Imagen/Veo на https://ai.google.dev/gemini-api/docs/. Единственное видео в Gemini API: `gemini-omni-1.1-flash` (GA с 2026-08-27, только Paid), через Interactions API.
3. У Gemini для всех моделей картинок и видео бесплатного тарифа нет ("Not available") [OK] https://ai.google.dev/gemini-api/docs/pricing. Пользователь с ключом без привязанной оплаты получит ошибку на первой генерации. Проверка ключа через `models.list` этого не покажет.
4. У OpenRouter есть собственные Images API и Videos API (отдельно от chat completions), с публичными каталогами и ценой по эндпоинтам [OK][CURL]. Это главный путь для видео и самый предсказуемый для цены.
5. Единственный вариант "подключить аккаунт" без копирования ключа для inference: OpenRouter OAuth PKCE. У OpenAI "Sign in with ChatGPT" не поддерживает image generation и рассчитан на open-source/локальные приложения. У Google санкционированного пути для сторонних приложений нет.
6. Баланс по ключу узнать нельзя ни у OpenAI, ни у Google. У OpenRouter `GET /api/v1/key` отдаёт `usage`, `limit`, `limit_remaining` по этому ключу, но не баланс аккаунта.
7. Заголовок `CF-IPCountry` через Cloudflare Tunnel документацией и экспериментом не подтверждён (см. 6.3). Для бота IP пользователя всё равно не виден.
8. Telegram Stars для цифровых услуг внутри Telegram не просто разрешены, а обязательны (см. 7). Это влияет на то, как продавать платные планы (если они будут) внутри бота и Mini App.

---

## 1. OpenAI

### 1.1 Эндпоинты картинок

- `POST /v1/images/generations` (JSON) и `POST /v1/images/edits` (multipart, либо JSON с `images[].image_url` / `file_id`) [OK] https://developers.openai.com/api/docs/guides/image-generation
- Responses API с tool `image_generation`: даёт multi-turn, `revised_prompt`, но дополнительно тарифицирует токены основной модели [OK] там же.
- Актуальные image-модели: `gpt-image-2.5-sunburst` (точность редактирования), `gpt-image-2.5-flare` (быстрая), `gpt-image-2`, `gpt-image-1.5`, `gpt-image-1-mini`. Закрыто: `gpt-image-1` с 2026-10-23, dall-e-2/3 с 2026-05-12. `gpt-image-1-mini` и `gpt-image-1.5` закрываются 2026-12-01 [OK] https://developers.openai.com/api/docs/deprecations.

### 1.2 Параметры и ответ

- Ответ GPT Image только `b64_json` (URL не бывает). В ответе поле `usage` (`input_tokens`, `output_tokens`, `input_tokens_details`) [OK].
- `n` 1-10, `prompt` до 32000 символов.
- `quality`: low / medium / high / xhigh / max / auto.
- `size`: 1024x1024, 1536x1024, 1024x1536, auto или произвольный WxH: кратно 16, соотношение сторон не больше 3:1, сторона не больше 3840, число пикселей от 655 360 до 8 294 400; выше 2560x1440 помечено экспериментальным [OK].
- `output_format`: png (по умолчанию), jpeg, webp; `output_compression` для jpeg/webp. Прозрачный фон только png/webp. `moderation`: auto / low.
- Сложный prompt может занимать до 2 минут [OK]. Cloudflare Proxy Read Timeout 125 с, поэтому запрос от браузера к нашему бэкенду синхронно держать нельзя (см. раздел 8).
- Edits: до 16 входных изображений, png/webp/jpg до 50 MB каждое; маска PNG до 4 MB, с alpha-каналом, того же размера, что изображение [OK].
- `safety_identifier`: хэш пользователя до 128 символов, рекомендован для мультипользовательских приложений; поле `user` deprecated [OK]. Передавать хэш нашего user_id, не Telegram id и не email.

### 1.3 Стриминг partial images

- Параметры `stream: true`, `partial_images` 0-3 [OK].
- События Image API: `image_generation.partial_image`, `image_generation.completed`; для edits `image_edit.*`. В Responses: `response.image_generation_call.partial_image` с полем `partial_image_b64`.
- Каждая partial добавляет примерно 100 image output tokens к цене.
- Для framy: стрим полезен только если мы сами проксируем SSE в браузер. Если воркер ждёт результат и кладёт в хранилище, partial не нужны. Решение: на первом этапе не использовать.

### 1.4 Цена

- Токенная: image input $8 / M, image output $30 / M, text input $5 / M; Batch вдвое дешевле [OK] https://developers.openai.com/api/docs/pricing.
- Для `gpt-image-2` фиксированные ориентиры: 1024x1024 low $0.006 / medium $0.053 / high $0.211; 1024x1536 и 1536x1024 low $0.005 / medium $0.041 / high $0.165 [OK].
- Для `gpt-image-2.5-*` токены на размер официально не опубликованы (калькулятор на JS, не извлёкся) [НЕ ПРОВЕРЕНО]. Вывод: оценку до запуска делать приблизительной (флаг `exact=false`), фактическую цену считать после ответа по `usage`.

### 1.5 Проверка ключа без трат

- `GET /v1/models` бесплатный. Невалидный ключ: HTTP 401, `invalid_request_error` [CURL]. Сообщение об ошибке эхо-возвращает префикс ключа, тело ответа в логи не писать.
- Спецификация описывает 403, если у ключа нет прав на список моделей. У ключей есть scopes (`api.model.request`, `api.all.read`, `api.files.read`) [OK]. Ключ с ограниченными правами может пройти `/models` и не пройти генерацию. Реальным ключом не проверялось [НЕ ПРОВЕРЕНО].
- Вывод: валидация при сохранении = `GET /v1/models`, но статус "ключ работает для картинок" ставить только после первой успешной генерации (или после классифицируемой ошибки).

### 1.6 Баланс и лимиты

- Баланс по обычному ключу узнать нельзя. `/v1/organization/costs` и usage требуют Admin API key, которым inference выполнять нельзя; просить Admin-ключ у пользователей не надо [OK] https://developers.openai.com/api/docs/api-reference/usage.
- Ошибки (https://developers.openai.com/api/docs/guides/error-codes) [OK]:
  - 401 invalid key; 401 IP not authorized (у пользователя включён IP allowlist, наш сервер не в нём);
  - 403 country/region not supported (по IP нашего сервера, не пользователя);
  - 403 "project does not have access to the requested model";
  - 429 `credit_balance_exhausted`, `organization_spend_limit_exceeded`, `project_spend_limit_exceeded`, `organization_usage_limit_exceeded`: не ретраить, показать пользователю текст "кончились деньги или лимит";
  - 429 `rate_limit_exceeded`, `slow_down`: ретраить с учётом `Retry-After`;
  - 400 `image_generation_user_error` / `moderation_blocked`: отказ модерации, не ретраить.
- Заголовки `x-ratelimit-*` в ответах.

### 1.7 Rate limits и уровни

- Уровни: Free / Build ($5) / Launch ($100) / Grow ($500). Для image-моделей в таблице лимитов указаны только Build / Launch / Grow: Build 250k TPM / 20 IPM, Launch 3M / 150, Grow 8M / 250 [OK] https://developers.openai.com/api/docs/models/gpt-image-2.5-flare. Вывод "у Free-уровня картинок нет, нужен хотя бы Build" это вывод из таблицы, не прямая цитата.

### 1.8 Organization Verification

- Гайд по изображениям говорит только, что может потребоваться API Organization Verification [OK]. Детали (Persona, документ, "1 ID на организацию за 90 дней") только по форумам [ФОРУМ]; страница help.openai.com отдала 403 [НЕ ПРОВЕРЕНО].
- Для UX: ошибка 403 про доступ к модели = показать "OpenAI требует верификацию организации для этой модели" со ссылкой на настройки пользователя.

### 1.9 Видео

- Нет. Videos API закрыт 2026-09-24 [OK]. Webhooks Sora теряют смысл. Responses `background: true` существует для текстовых/картиночных ответов с опросом `GET /v1/responses/{id}`.

### 1.10 Webhooks

- Standard Webhooks, настраиваются на уровне проекта в дашборде самого пользователя. Для BYOK бесполезны (мы не можем создать вебхук в чужом проекте). Использовать только синхронный запрос в воркере или опрос.

### 1.11 Страны

- Украина, Польша, Германия, Грузия, Молдова, Казахстан, UK, Швейцария в списке поддерживаемых; России и Беларуси нет [OK] https://developers.openai.com/api/docs/supported-countries. Ограничение применяется к IP, откуда идёт запрос, то есть к нашему серверу, не к пользователю.

---

## 2. Gemini API (Google AI Studio)

### 2.1 Два способа генерировать

- Interactions API: `POST https://generativelanguage.googleapis.com/v1beta/interactions`. GA с июня 2026, рекомендуется для новых проектов. Breaking changes в 2026 (steps вместо outputs, `response_format`), legacy-схема удалена 2026-06-08, нужен заголовок `Api-Revision` (пример `2026-05-20`) [OK] https://ai.google.dev/gemini-api/docs/interactions-overview, https://ai.google.dev/gemini-api/docs/interactions-breaking-changes-may-2026. Рекомендация: пиновать ревизию.
- `generateContent`: `POST /v1/models/{model}:generateContent`, документация называет его "legacy, but fully supported" [OK]. Для картинок это самый простой путь.

### 2.2 Картинки через generateContent

- Тело: `generationConfig: { responseModalities: ["TEXT","IMAGE"] (или ["Image"]), imageConfig: { aspectRatio, imageSize } }`. `imageSize` строго `"1K" | "2K" | "4K"` с заглавной K, `"512"` без K [OK] https://ai.google.dev/gemini-api/docs/image-generation.
- Ответ: `candidates[].content.parts[].inlineData.{mimeType, data}` (base64). Изображения-"мысли" (thought) не тарифицируются и приходят вместе с финальным.
- Те же настройки в Interactions: `response_format: { type: "image", mime_type, aspect_ratio, image_size }`.
- Референсных изображений до 14 (у lite-модели тоже до 14; у Pro лимиты по типам: объекты и персонажи отдельно).
- Модели и ограничения размера: `gemini-nano-banana-2.1` (512 нет), `gemini-3.1-flash-image` (есть 512), `gemini-3.1-flash-lite-image` (только 1K), `gemini-3-pro-image`, `gemini-2.5-flash-image` (документы противоречат: deprecations говорит shutdown 2027-03-15, страница pricing 2026-10-02) [OK, противоречие в источниках].

### 2.3 Цена картинок

- Токенов на картинку: 1K = 1120, 2K = 1680, 4K = 2520 (flash/2.1; в pricing для 2.1 4K = 3780), 512 = 747, Pro 4K = 2000 [OK] https://ai.google.dev/gemini-api/docs/pricing.
- Цена image output: $30 / M (2.1, lite) → $0.0336 за 1K, $0.0504 за 2K, $0.113 за 4K; $60 / M (3.1 Flash Image) → $0.045 / 0.067 / 0.101 / 0.151 за 512 / 1K / 2K / 4K. Batch вдвое дешевле.
- Для Gemini цена картинки почти фиксированная по размеру, в ответе есть `usageMetadata` для сверки.

### 2.4 Видео: Omni

- Модель `gemini-omni-1.1-flash`, только Paid, через Interactions API. Результат 3-10 с, 360p / 720p / 1080p / 4K (1080p и 4K upscaled, по умолчанию 720p), 24 FPS, со звуком. `response_format: { type: "video", aspect_ratio, resolution, delivery: "uri" }`. Явного параметра длительности нет [OK] https://ai.google.dev/gemini-api/docs/omni (и https://ai.google.dev/gemini-api/docs/video).
- Цена: input $1.50 / M, output video $17.50 / M при 5 792 токенов в секунду на 720p, то есть около $0.10 за секунду [OK].
- Ограничения: нет negative prompts, нет system instructions; SynthID-водяной знак. В EEA/CH/UK недоступны редактирование и продление загруженных видео и изображения с несовершеннолетними.

### 2.5 Асинхронность и получение видео

- Interactions синхронный по умолчанию. С `background: true` (требует `store: true`) возвращается id; опрос `GET /v1beta/interactions/{id}`; статусы `in_progress` / `requires_action` / `completed` / `failed` / `cancelled`; отмена `POST .../cancel`; удаление `DELETE` [OK].
- Видео в REST лежит в `steps[].content[]` как `{type:"video", mime_type, data(base64)}`. Поле `output_video` есть только в SDK.
- Для больше 4 MB: `delivery: "uri"`: создаётся файл `files/{id}`, опрос `GET /v1beta/files/{id}` до состояния `ACTIVE`, скачивание `files/{id}:download?alt=media`. GET interaction при этом всё равно возвращает base64 inline.
- Хранение: interactions Paid 55 дней (настраивается 7 / 14 / 28 / 55), Free 1 день; File API 48 часов. Скачивать результат сразу к себе.
- Webhooks Gemini: статические (на проект) и динамические (на запрос), событие `video.generated` [OK]. Для BYOK проще опрос.

### 2.6 Files API и inline-лимиты

- Inline до 100 MB на запрос (PDF 50 MB). Files API: 2 GB на файл, 20 GB на проект, хранение 48 часов [OK] https://ai.google.dev/gemini-api/docs/files.
- Для референсов картинок хватает inline (base64), Files API нужен только для больших видео-входов, которых у нас пока нет.

### 2.7 Проверка ключа

- `GET https://generativelanguage.googleapis.com/v1beta/models?pageSize=1000` (максимум 1000) с заголовком `x-goog-api-key` [OK]. Невалидный ключ: HTTP 400 `INVALID_ARGUMENT`, reason `API_KEY_INVALID` [CURL] (не 401).
- Использовать заголовок, не `?key=` в URL (ключ попадёт в логи).
- Тир определить нельзя. Free vs Paid проявляется только ошибкой 429 `RESOURCE_EXHAUSTED` при генерации. Форумы сообщают 429 с `limit: 0` на image-моделях у проектов без оплаты [ФОРУМ].
- С 2026-05-28 новые ключи из AI Studio делаются как auth-keys (привязаны к service account); неограниченные стандартные ключи Gemini API отвергает, ограниченные (restricted) продолжают работать [OK]. Пользователям нужна инструкция: какой ключ создавать.

### 2.8 Free tier и Paid

- Free для image/video "Not available" [OK] https://ai.google.dev/gemini-api/docs/pricing. Нужен Paid: Tier 1 = привязанный billing account; Prepay с минимальным пополнением $5; режимы Postpay/Prepay действуют с 2026-03-23.
- Лимиты привязаны к проекту, а не к ключу; суточный RPD сбрасывается в полночь по Pacific time; есть лимит IPM для картинок. Spend-лимит Tier 1 $10 за 10 минут (429 `RESOURCE_EXHAUSTED`) [OK] https://ai.google.dev/gemini-api/docs/rate-limits.

### 2.9 Регионы

- Украина, Польша, Германия, Грузия, Молдова, Казахстан, UK, Швейцария, Израиль доступны. России, Беларуси, Китая, Турции нет [OK] https://ai.google.dev/gemini-api/docs/available-regions. Определяется по IP нашего сервера. Если домашний сервер в Украине или ЕС, доступно.

### 2.10 Политика данных

- Terms: https://ai.google.dev/gemini-api/terms. Unpaid Services (бесплатная квота, AI Studio) дают Google право использовать данные для улучшения продуктов, рецензенты могут читать. В EEA/CH/UK условия Paid Services действуют даже на бесплатной квоте; Paid не используется для обучения, кратко логируется для abuse. API Client в EEA/CH/UK может использовать только Paid Services [OK].
- Сервис "для разработчиков, не для потребителей"; клиент не должен быть ориентирован на лиц младше 18 [OK].
- Логирование проекта по умолчанию включено для Interactions и для generateContent в проектах, созданных с 2026-10-26 (хранение 55 дней) [OK]. Это надо отразить в политике конфиденциальности framy.
- Вывод для нашего бесплатного тарифа: бесплатные картинки на наших ключах должны идти через платный (Paid) ключ Gemini, иначе их нет вообще; на нашем ключе данные пользователей попадут под условия Paid Services.

---

## 3. OpenRouter

### 3.1 Картинки

- Основной путь: `POST /api/v1/images` [OK] https://openrouter.ai/docs/api-reference/openapi.json (разобран OpenAPI).
  - Параметры: `model`, `prompt`, `resolution`, `aspect_ratio`, `size`, `quality`, `output_format`, `background`, `n` (1-10), `input_references`, `seed`, `stream`, `provider { only, order, ignore, sort, allow_fallbacks, options }`.
  - Ответ: `data[].b64_json` (+ `media_type`), `usage.cost`.
  - В `provider` нет `data_collection` и `zdr` (проверено по схеме `ImageGenerationProviderPreferences`). Для Images API per-request ZDR недоступен.
  - Стрим SSE (`image_generation.partial_image`, `completed`, `error`, `[DONE]`) работает для OpenAI-моделей.
  - Биллинг all-or-nothing, но при обрыве клиентом провайдер может списать с OpenRouter.
- Старый путь: `POST /api/v1/chat/completions` с `modalities: ["image","text"]` и `image_config`; ответ `choices[].message.images[].image_url.url` (data URL base64) [OK].
- Каталоги (все публичные, ключ не нужен) [CURL]:
  - `GET /api/v1/models?output_modalities=image`: 63 модели; кэш `max-age=120, stale-while-revalidate=3600`;
  - `GET /api/v1/images/models`: 61;
  - `GET /api/v1/videos/models`: 30;
  - `output_modalities=all`: 693.
- `GET /api/v1/models/user` фильтрует список по настройкам приватности пользователя (нужен его ключ).

### 3.2 Цена картинки (важная деталь)

- `/models` для не-токенных моделей отдаёт синтетическое поле `pricing.image_output`, которое не равно цене запроса. Честная цена в `GET /api/v1/images/models/{author}/{slug}/endpoints`: массив `pricing[]` с полями `billable`, `unit` (`token` | `image` | `megapixel`), `cost_usd`, `variant` [CURL]. У одной модели разные провайдеры с разной ценой (пример: `google-ai-studio` $0.00006 за токен против `google-vertex/us` $0.000066).
- Цена OpenRouter равна прайсу провайдера: проверено на примерах (google $60 / M, openai $30 / M), без наценки на inference [OK] https://openrouter.ai/docs/faq. Есть комиссия при покупке кредитов (точный размер в отрендеренной документации не нашёл) и BYOK-комиссия 5% сверх $25 000 в месяц [OK].
- Для детерминированной цены закреплять провайдера через `provider.only` (иначе fallback может выбрать более дорогой эндпоинт).

### 3.3 Видео

- `POST /api/v1/videos` → 202 `{ id, polling_url, status }`; `GET /api/v1/videos/{jobId}`; `GET /api/v1/videos/{jobId}/content?index=0`; `GET /api/v1/videos/models` [OK][CURL].
- Статусы: `pending` / `in_progress` / `completed` / `failed` (в webhook ещё `cancelled`, `expired`).
- Параметры: `model`, `prompt`, `duration`, `resolution`, `aspect_ratio`, `size`, `frame_images`, `input_references`, `generate_audio`, `seed`, `callback_url` (HTTPS), `provider.options`, `previous_job_id`.
- Результат: `unsigned_urls` (нужен API-ключ для скачивания) и подписанные `urls` с `expires_at`; `usage.cost`, `is_byok`.
- Опрос рекомендован примерно раз в 30 секунд.
- Webhooks: события `video.generation.completed | failed | cancelled | expired`, подпись `X-OpenRouter-Signature: t=...,v1=HMAC-SHA256("{t},{rawBody}")` (секрет в настройках workspace пользователя), `X-OpenRouter-Idempotency-Key`. Для BYOK-пользователей мы не можем настроить секрет в чужом workspace, поэтому опрос.
- Видео не подпадает под ZDR [OK].
- Цена видео в `/videos/models` приходит в разнородных SKU: `cents_per_video_output_second_720p` (центы), `duration_seconds_720p` (USD за секунду), `video_tokens` (USD за токен), `per-video-second`. Нужен нормализатор SKU → "USD за секунду при разрешении R" и тест на каждый SKU.

### 3.4 Ключ и баланс

- `GET /api/v1/key`: `label`, `limit`, `limit_remaining`, `limit_reset`, `usage` (+ `usage_daily/weekly/monthly`), `byok_usage`, `is_free_tier`, `free_model_daily_requests`, `budgets/effective_budget`, `expires_at`, `allowed_data_regions` [OK] https://openrouter.ai/docs/api-reference/limits. Невалидный ключ: 401 `{"error":{"message":"User not found.","code":401}}` [CURL].
- `GET /api/v1/credits` (баланс аккаунта) требует management key [OK]; обычный ключ получает 401. Баланс аккаунта по обычному или OAuth-ключу получить нельзя. При нехватке денег приходит 402 с `limit_source`: `openrouter_credits` | `openrouter_key_limit` | `openrouter_in_flight_budget`.
- Для показа пользователю: `limit_remaining` (если у ключа есть лимит) и `usage` (сколько потрачено через этот ключ).

### 3.5 Rate limits

- Платные модели без платформенного лимита. Бесплатные модели: 20 запросов в минуту и 50 в день, пока куплено меньше 10 кредитов [OK]. 429 различает ответ OpenRouter и upstream-провайдера.

### 3.6 OAuth PKCE (подробно)

Источник: https://openrouter.ai/docs/guides/overview/auth/oauth [OK], часть проверена запросами [CURL].

- Регистрация приложения не нужна.
- Шаг 1, редирект пользователя:
  `https://openrouter.ai/auth?callback_url=<URL>&code_challenge=<S256>&code_challenge_method=S256&state=<...>&key_label=framy`
  Опционально `workspace_id`, `required_workspace_id`. `callback_url` только HTTPS или localhost любого порта; кастомные схемы (`tg://`, `myapp://`) не поддерживаются.
- Шаг 2, обмен кода: `POST https://openrouter.ai/api/v1/auth/keys` с телом `{ "code": "...", "code_verifier": "...", "code_challenge_method": "S256" }`. Ответ `{ "key": "sk-or-v1-...", "user_id": "..." }`. Это обычный API-ключ, оплата идёт с кредитов пользователя.
- Код одноразовый, живёт 10 минут; ошибки 400 / 403 / 405.
- Проверено: `/auth?...` отвечает 307 [CURL]; обмен с мусорным кодом даёт 400 "Invalid code" [CURL].
- Headless-режим: если не передать `callback_url`, код показывается на экране, пользователь вставляет его в приложение (`code_challenge` обязателен). Подходит для чата бота.
- Приложение идентифицируется по hostname `callback_url`; заголовки `HTTP-Referer` и `X-OpenRouter-Title` для рейтингов приложений.
- Отзыв: пользователь отзывает ключ сам на openrouter.ai/settings/keys; программного удаления без management key нет. Есть `POST /auth/keys/code` (параметры `limit`, `expires_at`, `usage_limit_type`) для ограничения ключа. `/oauth/token` относится к workload identity для организаций и к framy не относится.
- Для framy: сервер генерирует `code_verifier` и `state`, кладёт в Postgres с TTL 10 минут, привязывает к user_id; `callback_url` ведёт на наш домен (`https://framy.../oauth/openrouter/callback`); на callback сервер сверяет `state` и обменивает код. В Mini App открывать через `Telegram.WebApp.openLink` (внешний браузер). Для бота без Mini App: headless-вариант с вставкой кода.

### 3.7 Приватность: ZDR и data_collection

- В chat completions: `provider.data_collection: "allow" | "deny"` и `provider.zdr: true` (ИЛИ с настройками аккаунта и guardrails) [OK] https://openrouter.ai/docs/guides/routing/provider-selection.
- Для Images API этих полей в схеме нет; для Videos ZDR не применим. Настройки приватности аккаунта пользователя могут сужать доступные эндпоинты (поэтому `/models/user`).
- Per-model-group ZDR (Anthropic, OpenAI, Google и др.) убирает первопартийные эндпоинты.

### 3.8 BYOK внутри OpenRouter

- Пользователь может привязать в OpenRouter ключи провайдеров (приоритет, fallback, "Never use shared capacity"). Для нас прозрачно: смотреть `usage.is_byok`; цена и комиссия при этом другие (платит сам провайдер, OpenRouter берёт BYOK-комиссию после порога).

---

## 4. Аналоги OAuth "подключить аккаунт" у OpenAI и Google

- OpenAI: "Sign in with ChatGPT" (SIWC, запуск около 2026-09-29, scope `chatgpt.tokens.use.direct`) даёт использование квоты плана Plus/Pro. Но: image generation в списке неподдерживаемых инструментов (рядом file search, Code Interpreter и др.), требует `store: false` и `stream: true`, предназначен для open-source и локальных приложений; платные и облачные сервисы идут через форму интереса [OK] https://developers.openai.com/siwc/token-sharing-open-source, https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations. Для framy неприменимо.
- Google: есть OAuth-quickstart для Gemini API, но он тестовый (Cloud-проект, consent screen, scopes `cloud-platform`, `generative-language.retriever`, предупреждение "Google hasn't verified this app") [OK] https://ai.google.dev/gemini-api/docs/oauth. Санкционированного способа для стороннего приложения брать квоту пользователя нет. Прокси токенов CLI запрещён по вторичным источникам [ФОРУМ].
- Итог: OpenRouter единственный провайдер с нормальным OAuth для сторонних приложений; OpenAI и Gemini через вставку ключа.

---

## 5. Хранение чужих ключей

Источники: OWASP Secrets Management и Cryptographic Storage cheat sheets, Google KMS envelope encryption, `crypto/cipher` (Go), tink-go [OK] (URL в приложении).

- Envelope encryption: на каждую запись свой DEK, шифрование AES-256-GCM, в AAD класть `user_id | provider | key_id`, чтобы шифртекст нельзя было перенести на другую запись. DEK шифруется KEK; KEK не в БД (переменная окружения, Docker secret или systemd-creds), с номером версии `kek_version` для ротации.
- В БД: `ciphertext`, `nonce`, `wrapped_dek`, `kek_version`, `last4`, `status`, `created_at`, `last_ok_at`. Показывать пользователю только `last4`.
- Не логировать ключи и тела ошибок провайдеров (OpenAI эхо-возвращает префикс ключа). Маскировать заголовки `Authorization`, `x-goog-api-key`.
- Валидировать при сохранении (раздел 1.5, 2.7, 3.4). Отзыв: удалить у себя (hard delete, а не флаг); у OpenAI, Google и OpenRouter ключ отзывает сам пользователь, и надо так и написать в интерфейсе.
- Ключ расшифровывать только в воркере на время запроса; не отдавать в браузер и не передавать в Mini App.
- Если пользователь ввёл ключ в чат бота, сообщение с ключом бот должен удалить (`deleteMessage`), но не гарантированно (Telegram хранит у себя); для ключей лучше вводить в Mini App/сайте.

---

## 6. Валюта и страна

### 6.1 Курсы НБУ

- Эндпоинт: `https://bank.gov.ua/NBUStatService/v1/statdirectory/exchange?json` (все валюты, 45 штук), `&valcode=USD`, `&date=YYYYMMDD` [CURL][OK] https://bank.gov.ua/ua/open-data/api-dev.
- Формат: `[{"r030":840,"txt":"Долар США","rate":45.5382,"cc":"USD","exchangedate":"27.10.2026","special":"N"}]`. `rate` = сколько UAH за 1 единицу валюты (JPY 0.2895, то есть за 1 иену). Поле `special` (Y/N) только у USD (с 2026-01-21).
- Будущая дата и неизвестный код дают `[]`. Выходные имеют курс (24.10 даёт 45.051).
- Правило публикации: до 15:30 по Киеву отдаётся курс текущего рабочего дня, после 15:30 курс следующего. Проверка сегодня в 20:08 UTC 26.10: уже отдаёт `exchangedate` 27.10, USD 45.5382, EUR 50.9527 [CURL].
- Нет в списке: BYN, BRL, ISK, PHP и другие. Есть: AED, AUD, AZN, BDT, CAD, CHF, CNY, CZK, DKK, DZD, EGP, EUR, GBP, GEL, HKD, HUF, IDR, ILS, INR, JPY, KRW, KZT, LBP, MDL, MXN, MYR, NOK, NZD, PLN, RON, RSD, SAR, SEK, SGD, THB, TND, TRY, USD, VND, XAG, XAU, XDR, XPD, XPT, ZAR.
- Считать так: курс USD→X = rate(USD) / rate(X) в UAH (кросс-курс через гривну). USD→UAH = rate(USD).
- Частота: достаточно одного запроса в день плюс пересчёт после 15:30 по Киеву; хранить в таблице `fx_rates (date, code, uah_per_unit, source, fetched_at)`; при недоступности брать последний сохранённый, показывать дату курса.

### 6.2 Запасные источники

- `fawazahmed0/currency-api`: лицензия CC0, без лимитов, 339 валют, UAH есть. URL: `https://cdn.jsdelivr.net/npm/@fawazahmed0/currency-api@latest/v1/currencies/usd.min.json`, запасной `https://latest.currency-api.pages.dev/v1/currencies/usd.min.json`. Обновление ежедневное. USD→UAH на момент проверки 45.376 [CURL].
- `open.er-api.com/v6/latest/USD`: ключ не нужен, 166 валют, UAH есть, обновляется раз в день, требует атрибуции и запрещает редистрибуцию. USD→UAH 45.274 [CURL].
- Не годятся: Frankfurter (`api.frankfurter.dev`) и ECB XML (`eurofxref-daily.xml`) содержат 29-30 валют, UAH нет [CURL]; exchangerate.host требует ключ (`missing_access_key`) [CURL].
- Порядок: НБУ → fawazahmed0 (jsDelivr) → fawazahmed0 (pages.dev) → open.er-api.com → последний сохранённый.
- Курсы источников расходятся на доли процента (45.54 / 45.38 / 45.27); показывать как "примерно" и с датой.

### 6.3 CF-IPCountry через Cloudflare Tunnel

- Включение: Dashboard → Network → IP Geolocation = On (или API, PATCH настройки `ip_geolocation` = "on"); либо Managed Transform "Add visitor location headers" (`cf-ipcountry`, `cf-ipcity`, `cf-region`, `cf-timezone` и др.). Доступно на Free-плане [OK] https://developers.cloudflare.com/network/ip-geolocation/, https://developers.cloudflare.com/rules/transform/managed-transforms/reference/.
- Значения: ISO-3166 alpha-2, `XX` (нет данных), `T1` (Tor).
- Через Tunnel: документация говорит, что DNS-запись тоннеля это CNAME на `<UUID>.cfargotunnel.com` и трафик проксируется Cloudflare [OK] https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/routing-to-tunnel/dns/. Логично, что заголовки добавляет edge до отправки в тоннель, но прямого подтверждения в документации нет, а эксперимент с quick tunnel был отклонён разрешениями и не выполнялся [НЕ ПРОВЕРЕНО].
- Безопасная проверка при разработке: в сервисе залогировать `CF-IPCountry` и `CF-Connecting-IP` на запросе с реального домена за тоннелем.
- Для бота: запросы к нашему бэкенду приходят от серверов Telegram (webhook) или из Mini App (браузер пользователя). В webhook IP пользователя не виден. Валюту для бота брать из сохранённой настройки пользователя, при её отсутствии из `from.language_code` (Telegram даёт в User и в Mini App init data) [OK] https://core.telegram.org/bots/api#user. `language_code` определяет язык, не страну, поэтому для валюты это слабый сигнал (uk → UAH, ru → неоднозначно); по умолчанию USD, и спросить один раз.
- Mini App: запросы идут из браузера пользователя через Cloudflare, `CF-IPCountry` доступен так же, как на сайте.
- Cloudflare лимиты соединений: Proxy Read Timeout 125 с (ошибка 524), Write 30 с, Idle 900 с [OK] https://developers.cloudflare.com/fundamentals/reference/connection-limits/.

---

## 7. Telegram Stars для цифровых услуг

- Да, и это требование. Все платежи за цифровые товары и услуги внутри Telegram (боты, Mini Apps) должны идти только в Stars (валюта `XTR`); иначе бот или Mini App может стать недоступным в сторовых версиях приложения [OK] https://core.telegram.org/bots/payments-stars, https://telegram.org/tos/bot-developers (раздел 6.2).
- Обязательные элементы: команды `/terms` и `/paysupport`, обработка `pre_checkout_query`, проверка `successful_payment`, возврат через `refundStarPayment`.
- Stars конвертируются в награды через Fragment (не собственность; задержка до 21 дня; комиссия 15% при включённых topics в личных чатах) [OK].
- Для framy: если платные планы продаются внутри бота или Mini App, платить нужно Stars. На сайте вне Telegram можно принимать другие способы, но это вне данного исследования. Покупка кредитов у OpenAI/Google/OpenRouter происходит у них и к Stars не относится: пользователь платит им напрямую (BYOK).
- Что остаётся неясным: не нашёл прямой формулировки, считается ли оплата подписки на сайт из Mini App "внутри Telegram" (откроется внешний браузер через `openLink`). Не проверено.

---

## 8. Выводы для framy

### 8.1 Подключение провайдеров

| Провайдер | Способ подключения | Валидация при сохранении | Что покажет реальные возможности |
|---|---|---|---|
| OpenRouter | OAuth PKCE (основной), ручной ключ (запасной) | `GET /api/v1/key` | 402 на нехватке кредитов, 403 по правилам приватности |
| OpenAI | ручной ключ | `GET /v1/models` | 403 по доступу к модели, 429 `credit_balance_exhausted`, 403 organization verification |
| Gemini | ручной ключ (инструкция: Paid, restricted/auth key) | `GET /v1beta/models?pageSize=1000` | 429 `RESOURCE_EXHAUSTED` с `limit: 0` = ключ без оплаты |

Рекомендация: OpenRouter ставить первой кнопкой "Подключить в один клик". OpenAI и Gemini давать как "Вставить ключ" с пошаговой инструкцией. Статус ключа: `valid` (прошла проверка списка), `verified_generation` (была успешная генерация), `needs_billing` / `needs_verification` / `revoked` (по классифицированным ошибкам), чтобы не обещать пользователю то, что мы проверить не можем.

OAuth в Mini App: `Telegram.WebApp.openLink` открывает внешний браузер; после callback на нашем домене показать "вернитесь в Telegram" и обновить статус через опрос или deep link `https://t.me/<bot>?start=...`. `openLink` есть в WebApp API [OK] https://core.telegram.org/bots/webapps.

### 8.2 Единый слой provider → model → price

Схема таблиц (предложение):
- `provider_connections`: `id`, `user_id`, `provider` (`openai` / `gemini` / `openrouter`), `ciphertext`, `nonce`, `wrapped_dek`, `kek_version`, `last4`, `status`, `last_ok_at`, `last_error_class`.
- `model_catalog`: каноническая модель (`canonical_id`, `kind` image/video, возможности: размеры, референсы, длительность, звук, аспекты), версионируется данными, а не кодом, с флагом `disabled_at` для быстрого отключения (закрытия моделей идут часто: Sora, Imagen, Veo, gpt-image-1 закрыты за последний год).
- `model_offers`: `canonical_id`, `provider`, `provider_model_id`, `pricing_spec` (JSON с типом), `source` (static/dynamic), `fetched_at`, `exact`.
- `pricing_spec` бывает разных видов: `per_image{size,quality→usd}`, `per_token{input,output, tokens_by_size}`, `per_second{resolution→usd}`, `per_megapixel`, `per_video_token`. Нормализатор приводит все SKU OpenRouter к этим видам.
- Источники цен: OpenRouter динамически (`/images/models/{id}/endpoints`, `/videos/models` с нормализатором, закрепление `provider.only`); OpenAI и Gemini статические версионируемые таблицы (число токенов на размер × $/токен); сверка со значениями OpenRouter (его цена равна прайсу провайдера), расхождение выводит предупреждение в лог.
- Оценка и факт: до запуска показываем оценку (`estimate_usd`, `exact` true/false), после ответа записываем фактическую цену из `usage` (OpenAI `usage` токены × тариф; Gemini `usageMetadata`; OpenRouter `usage.cost`). Хранить в `generations`: `estimate_usd`, `actual_usd`, `fx_rate`, `fx_date`.
- Показ вариантов: если модель есть у нескольких подключённых провайдеров, строка на каждого провайдера с ценой в USD и в валюте пользователя; сортировка по цене; для токенных цен подпись "примерно".
- Валюта: `display_currency` в профиле пользователя, по умолчанию по `CF-IPCountry` (после проверки в 6.3), иначе USD; пересчёт курсом НБУ на момент показа, курс дата фиксируется в записи генерации.

### 8.3 Асинхронные задачи и опрос

- Все генерации идут через job-таблицу в Postgres: `jobs(id, user_id, connection_id, provider, model, request, status, provider_job_id, result_url, estimate_usd, actual_usd, attempts, next_poll_at, idempotency_key)`. API на POST отвечает 202 и `job_id` сразу: Cloudflare обрывает запрос на 125 с, а картинки OpenAI могут идти до 2 минут, видео минуты.
- Воркеры Go: картинки выполняем синхронным запросом к провайдеру в воркере (таймаут 150-180 с); видео через опрос: OpenRouter `GET /videos/{id}` раз в ~30 с, Gemini `GET /interactions/{id}` (с `background: true`) и `GET /files/{id}`.
- Клиенту: SSE или короткий polling `GET /jobs/{id}`; для бота воркер сам шлёт результат сообщением.
- Результат скачивать сразу в своё хранилище: Gemini files живут 48 часов, interaction 1 или 55 дней, `unsigned_urls` OpenRouter требуют ключ, подписанные истекают.
- Идемпотентность: ретраи могут привести к двойному списанию у пользователя. Ключ идемпотентности на job; повторять только на безопасных ошибках (429 rate limit, 5xx до начала генерации); не повторять после тайм-аута без проверки статуса (у OpenRouter и Gemini есть id задачи); 429 по деньгам и moderation не повторять.
- Webhooks провайдеров для BYOK не использовать (секреты и вебхуки настраиваются в аккаунте пользователя).

### 8.4 Бесплатный тариф на наших ключах

- Оценка порядка величин: картинка $0.03-0.07 (Nano Banana 2 Lite 1K $0.0336, qwen-image-3 $0.03, recraft-v4 $0.04), 5 в день на пользователя примерно $0.15-0.35. Видео 4-5 секунд примерно $0.15-0.25 (veo-3.1-lite 720p без аудио $0.03/с, wan-3.0 480p $0.05/с). Цифры из каталогов OpenRouter на 2026-10-26, не прайс-лист; пересчитать на реальной выборке моделей.
- Для бесплатных картинок на наших ключах использовать самую дешёвую модель через OpenRouter с лимитом дневного бюджета на ключ; лимиты `limit`/`limit_reset` на нашем ключе OpenRouter как предохранитель от расхода.
- Gemini бесплатного тира для картинок не имеет, поэтому собственный Gemini-ключ для бесплатных запросов должен быть платным.

### 8.5 Главные риски

1. Быстрая смена API в 2026: Sora, Imagen, Veo 3.1 preview, gpt-image-1 закрыты; Interactions API с обязательным `Api-Revision`. Каталог моделей данными и мониторинг deprecations (раз в неделю читать страницы deprecations OpenAI/Google).
2. Токенная цена у OpenAI и Gemini: оценка до запуска приблизительна для `gpt-image-2.5-*`, фактическая цена известна только после ответа.
3. Gemini: нет free tier для картинок и видео; ошибка проявляется только при генерации; в EEA/CH/UK только Paid Services; interactions хранятся 55 дней.
4. OpenRouter Images API без per-request ZDR; видео вне ZDR. Нельзя обещать пользователю "данные не сохраняются" для этих режимов.
5. Abuse и политика платформ: `safety_identifier` (хэш пользователя) у OpenAI; Google требует 18+; модерация и жалобы; ограничения дорогих запросов (n до 10, 4K, длинное видео) на нашей стороне, потому что платит пользователь, но блокировки ключей придут на его ключ.
6. IP-ограничения: у пользователя может быть IP allowlist (OpenAI 401 "IP not authorized"), а страна нашего сервера определяет доступность OpenAI и Gemini. Домашний сервер должен выходить через IP в поддерживаемой стране (Украина, ЕС).
7. Нет способа узнать баланс: показывать понятные тексты на 402/429 и подсказку "пополните у провайдера".
8. Хранение ключей: утечка БД без KEK не раскрывает ключи, утечка KEK и БД раскрывает все; ограничить доступ к KEK, ротация, аудит расшифровки.
9. `CF-IPCountry` через Tunnel не проверен (6.3): до проверки не строить на нём логику, только подсказку по умолчанию.
10. Telegram Stars: платные планы внутри Telegram только за Stars; проверить на этапе дизайна монетизации, прежде чем делать внешнюю оплату.

### 8.6 Что не проверено

- Поведение платных эндпоинтов OpenAI, Gemini и OpenRouter с реальным ключом (все проверки идут по документации и по публичным эндпоинтам).
- Organization Verification (детали): help.openai.com отдаёт 403.
- Токены и цена `gpt-image-2.5-*` по размерам.
- `CF-IPCountry` за Cloudflare Tunnel.
- Размер комиссии OpenRouter при покупке кредитов.
- Противоречие в сроке закрытия `gemini-2.5-flash-image` (2027-03-15 в deprecations и 2026-10-02 на pricing).
- Считается ли оплата на внешнем сайте из Mini App "внутри Telegram" с точки зрения правил Stars.

---

## Приложение: источники

OpenAI
- https://developers.openai.com/api/docs/guides/image-generation
- https://developers.openai.com/api/docs/guides/video-generation
- https://developers.openai.com/api/docs/deprecations
- https://developers.openai.com/api/docs/pricing
- https://developers.openai.com/api/docs/guides/error-codes
- https://developers.openai.com/api/docs/models/gpt-image-2.5-flare
- https://developers.openai.com/api/docs/supported-countries
- https://developers.openai.com/siwc/token-sharing-open-source
- https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations

Gemini
- https://ai.google.dev/gemini-api/docs/image-generation
- https://ai.google.dev/gemini-api/docs/omni
- https://ai.google.dev/gemini-api/docs/video
- https://ai.google.dev/gemini-api/docs/deprecations
- https://ai.google.dev/gemini-api/docs/logs-policy
- https://ai.google.dev/gemini-api/docs/billing
- https://ai.google.dev/gemini-api/docs/interactions-overview
- https://ai.google.dev/gemini-api/docs/pricing
- https://ai.google.dev/gemini-api/docs/rate-limits
- https://ai.google.dev/gemini-api/docs/files
- https://ai.google.dev/gemini-api/docs/available-regions
- https://ai.google.dev/gemini-api/docs/oauth
- https://ai.google.dev/gemini-api/terms

OpenRouter
- https://openrouter.ai/docs/api-reference/openapi.json
- https://openrouter.ai/docs/guides/overview/auth/oauth
- https://openrouter.ai/docs/api-reference/limits
- https://openrouter.ai/docs/guides/routing/provider-selection
- https://openrouter.ai/docs/faq
- https://openrouter.ai/api/v1/models, /images/models, /videos/models, /key (проверялись curl)

Валюта и Cloudflare
- https://bank.gov.ua/ua/open-data/api-dev
- https://bank.gov.ua/NBUStatService/v1/statdirectory/exchange?json
- https://cdn.jsdelivr.net/npm/@fawazahmed0/currency-api@latest/v1/currencies/usd.min.json
- https://open.er-api.com/v6/latest/USD
- https://developers.cloudflare.com/network/ip-geolocation/
- https://developers.cloudflare.com/rules/transform/managed-transforms/reference/
- https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/routing-to-tunnel/dns/
- https://developers.cloudflare.com/fundamentals/reference/connection-limits/

Telegram
- https://core.telegram.org/bots/payments-stars
- https://telegram.org/tos/bot-developers
- https://core.telegram.org/bots/webapps
- https://core.telegram.org/bots/api#user

Хранение ключей
- https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html
- https://cheatsheetseries.owasp.org/cheatsheets/Cryptographic_Storage_Cheat_Sheet.html
- https://cloud.google.com/kms/docs/envelope-encryption
- https://pkg.go.dev/crypto/cipher#NewGCM
- https://github.com/tink-crypto/tink-go

