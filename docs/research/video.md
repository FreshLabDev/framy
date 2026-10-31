# Генерация видео через OpenAI, Gemini API и OpenRouter: срез на 2026-10-26

Исследование для framy. Все данные сняты 2026-10-26. Главный источник: `GET https://openrouter.ai/api/v1/videos/models` (публичный, без ключа); сырые выгрузки не сохранялись.

## 1. Коротко

1. **OpenAI: видео через API больше нет.** Videos API, `sora-2`, `sora-2-pro` и все датированные снимки отключены 2026-09-24, замена не названа. Ключ OpenAI для видео в framy бесполезен.
2. **Gemini API: одна модель, `gemini-omni-1.1-flash`**, через Interactions API (не `generate_videos`). Все Veo в Gemini API отключены (Veo 3.1 preview 2026-10-22, четыре дня назад). Free tier у Omni нет, только платный. Длительность 3-10 с, явного параметра в гайде нет, выбирает модель.
3. **OpenRouter: 30 видеомоделей** и один асинхронный API (`POST /api/v1/videos`). Veo 3.1 / Fast / Lite доступны там (через Google Vertex), хотя из Gemini API их убрали. Есть Kling v3, Seedance 2.0/2.5, Wan 3.0, Hailuo 3, Grok Imagine 1.5, Runway Gen-4.5, FLUX 3 Video, HeyGen. Sora, Luma, Pika, Midjourney в списке нет.
4. Каталог моделей с допустимыми длительностями, разрешениями, форматами, кадрами и ценами OpenRouter отдаёт **публично, без ключа**: `GET https://openrouter.ai/api/v1/videos/models`. Интерфейс framy можно строить из него, а не из зашитого списка. Список меняется быстро: в cookbook OpenRouter ещё значится `openai/sora-2-pro`, в живом ответе его уже нет; `seedance-1-5-pro` имеет `expiration_date` 2026-11-11.
5. Для бесплатной недельной генерации по цене и слепому рейтингу Artificial Analysis подходят `heygen/heygen-video-1`, `x-ai/grok-imagine-video-1.5-lite`, `minimax/hailuo-3-max` (раздел 7).

## 2. Пометки достоверности

| Пометка | Значение |
|---|---|
| [ОФ] | прочитано на официальной странице документации, URL указан |
| [API] | живой ответ API OpenRouter 2026-10-26 (машинные данные самого провайдера) |
| [ВТ] | вторичный источник (блоги, ресселлеры, агрегаторы), не подтверждено официально |
| [РАСЧЁТ] | посчитано мной из данных выше, формула приведена |
| [?] | не удалось проверить; цифру не выдумываю |

Единицы в `pricing_skus` OpenRouter нигде формально не описаны (в схеме написано только "Pricing SKUs with provider prefix stripped"). Я определил их сопоставлением с тем, что OpenRouter показывает на страницах моделей, и с официальной ценой xAI: ключи `cents_per_*` в центрах, `duration_seconds*` в USD за секунду, `video_tokens*` в USD за токен. Совпало везде, где было с чем сравнить (Grok Lite: SKU 2 -> "Price from $0.02/second" на OpenRouter и "$0.020 / sec" у xAI; Seedance 2.0: расчёт 5 с 1080p = $1.87 совпал с примером BytePlus).

## 3. OpenAI

### 3.1 Статус [ОФ]

- https://developers.openai.com/api/docs/guides/video-generation (заголовок страницы): "The Sora 2 models and Videos API were shut down on September 24, 2026 and are no longer available. No one-to-one replacement API is available."
- https://developers.openai.com/api/docs/deprecations, запись 2026-03-24: отключены Videos API, `sora-2`, `sora-2-pro`, `sora-2-2025-10-06`, `sora-2-2025-12-08`, `sora-2-pro-2025-10-06`; колонка "Recommended replacement" везде "-".
- В списке моделей и на странице цен (`/api/docs/models`, `/api/docs/pricing`) видеомоделей нет. Поиск по вторичным источникам нового видео-API OpenAI не нашёл (упоминается внутренний проект "Spud", не видео и не выпущен) [ВТ].
- Потребительское приложение Sora закрыто 2026-04-26 [ВТ].

### 3.2 Что было (только для справки, работать не будет) [ОФ, гайд оставлен как архивный]

| Пункт | Значение |
|---|---|
| Модели | `sora-2` (быстрая, итерации), `sora-2-pro` (качество, 1080p) |
| Эндпоинты | `POST /v1/videos`, `GET /v1/videos/{id}`, `GET /v1/videos/{id}/content?variant=video|thumbnail|spritesheet`, `GET /v1/videos`, `DELETE /v1/videos/{id}`, `POST /v1/videos/characters`, `POST /v1/videos/extensions`, `POST /v1/videos/edits`, ремикс `POST /v1/videos/{id}/remix` (объявлен устаревшим 2026-03-12) |
| Параметры | `model`, `prompt`, `size` (`1280x720`, `720x1280`, 1080p `1920x1080`/`1080x1920` только у pro), `seconds` (строка; 16 и 20 поддерживались с 2026-03-12), `input_reference` (первый кадр, JPEG/PNG/WebP, размер должен совпасть с `size`), `characters[{id}]` (до 2 на видео, не люди) |
| Продление | до 20 с за шаг, до 6 шагов, всего 120 с |
| Статусы | `queued`, `in_progress`, `completed`, `failed`, поле `progress` в процентах; вебхуки `video.completed`, `video.failed` |
| Ссылка на файл | живёт максимум 1 час после генерации |
| Ограничения | реальные люди (в том числе публичные) нельзя; входные фото с лицами людей отклоняются; персонажи с человеческим обликом заблокированы; чужие персонажи и музыка отклоняются; контент для аудитории младше 18 |
| Цена | 1080p у `sora-2-pro` $0.70/с [ОФ, changelog 2026-03-12]; остальные цены на текущих страницах отсутствуют [?] |

Водяные знаки в этом гайде не упоминаются.

## 4. Gemini API (Google AI Studio)

### 4.1 Статус моделей [ОФ] https://ai.google.dev/gemini-api/docs/deprecations

| Модель | Релиз | Отключение | Замена |
|---|---|---|---|
| `veo-2.0-generate-001`, `veo-3.0-generate-001`, `veo-3.0-fast-generate-001` | 2025 | 2026-06-30 | `gemini-omni-1.1-flash` или GA на Gemini Enterprise Agent Platform |
| `veo-3.1-generate-preview`, `veo-3.1-fast-generate-preview` | 2025-10-15 | 2026-10-22 | то же |
| `veo-3.1-lite-generate-preview` | 2026-03-31 | 2026-10-22 | то же |
| `gemini-omni-flash-preview` | 2026-06-30 | 2026-10-22 | `gemini-omni-1.1-flash` |
| **`gemini-omni-1.1-flash`** | **2026-08-27 (GA)** | не объявлено | - |

Страница https://ai.google.dev/gemini-api/docs/video: "The Veo 3.1 Preview in the Gemini API has been shut down." Veo GA остаётся на Gemini Enterprise Agent Platform (аналог Vertex AI, отдельная авторизация, не ключ AI Studio). Через OpenRouter те же Veo 3.1 идут с провайдером `google-vertex` и оплачиваются кредитами OpenRouter.

### 4.2 `gemini-omni-1.1-flash`

Источники: https://ai.google.dev/gemini-api/docs/omni (текст получен как `.md.txt`), https://ai.google.dev/gemini-api/docs/models/gemini-omni-flash, https://ai.google.dev/api/interactions-api, https://ai.google.dev/gemini-api/docs/pricing, https://ai.google.dev/gemini-api/docs/changelog (запись 2026-08-27). Все [ОФ], если не помечено иначе.

**Запрос.** `POST https://generativelanguage.googleapis.com/v1beta/interactions` (заголовок `x-goog-api-key`; в примерах также `?key=`). SDK: `client.interactions.create(...)`, готовое видео в `interaction.output_video` (только SDK); в сыром REST видео лежит в `steps[].content[]` с `type: "video"`, поле `data` (base64) или `uri`.

| Параметр | Допустимые значения | Примечание |
|---|---|---|
| `model` | `gemini-omni-1.1-flash` | |
| `input` | строка или массив частей: `{"type":"text","text"}`, `{"type":"image","data"\|"uri","mime_type"}`, `{"type":"video","uri"\|"data"}` | видео для правки/продления грузить через Files API |
| `response_format.type` | `"video"` | необязательно |
| `response_format.aspect_ratio` | `16:9` (по умолчанию), `9:16` | других нет |
| `response_format.resolution` | `360p`, `720p` (по умолчанию), `1080p`, `4k` | 1080p и 4k получаются апскейлом |
| `response_format.duration` | строка | поле есть в справочнике ("The duration for the video output"), формат и допустимые значения не описаны, в гайде не используется [?] |
| `response_format.delivery` | `inline` (base64 в ответе), `uri` (ссылка Files API) | для видео больше 4 МБ рекомендован `uri` |
| `generation_config.video_config.task` | `text_to_video`, `image_to_video`, `reference_to_video`, `edit`, `extend` | необязательно; модель выводит режим из промпта; Google советует использовать `task` только если промпт не сработал, "task adds strict constraints" |
| `previous_interaction_id` | id прошлого interaction | правка и продление сгенерированного видео без повторной загрузки |
| `store` | bool, по умолчанию `true` | `false` быстрее, но ломает `previous_interaction_id` и `background` |
| `background` | bool | асинхронный режим для долгих задач (HTTP-соединения обычно закрываются через 60 с); явно для Omni в списке поддерживаемых моделей на странице background не назван [?] |
| `webhook_config` | `uris[]` | вебхуки для Interactions API, есть в справочнике |

**Не поддерживается:** negative prompt (писать в обычный промпт: "No dialogue", "No extra sound effects"), seed, temperature, top_p, stop sequences, system instructions, аудио-референсы, provisioned throughput, количество вариантов, выбор fps (24 fps по карточке модели).

**Режимы:**

| Режим | Как |
|---|---|
| Текст -> видео | `input` строкой |
| Картинка -> видео (первый кадр) | одна картинка + текст; если не указано иначе, модель сама решает, как использовать картинку; теги `<FIRST_FRAME>` или `task: image_to_video` фиксируют роль |
| Первый + последний кадр | две картинки, теги `<FIRST_FRAME> <LAST_FRAME>`; одна и та же картинка в обеих ролях даёт петлю |
| Референсы | картинки (`<IMAGE_REF_0>`, `<IMAGE_REF_1>`...), на странице есть пример с 6 картинками, предел не назван; видео-референсы `<VIDEO_REF_N>`: максимум 3 клипа по 3 с, звук в референсах игнорируется |
| Правка своего видео | видео до 10 с через Files API; недоступно в ЕЭЗ, Швейцарии и Великобритании |
| Диалоговая правка | `previous_interaction_id` + текст ("Make the violin invisible. Keep everything else the same.") |
| Продление (extend) | добавляет в конец клипа; в разделе про extension написано "3-10 second continuation", в разделе про промпты "extend by 10s, up to a total length of 40s"; загруженное видео до 10 с; нельзя добавить речь в загруженное видео, где уже говорят (в multi-turn можно) |
| Ремикс | отдельного режима нет, это `edit` |

**Звук.** Генерируется всегда; переключателя нет, управление промптом ("Include calm background music", "No dialogue"). На цену не влияет (цена по токенам видео).

**Асинхронность и результат.**
- По умолчанию вызов синхронный (unary): ответ приходит, когда видео готово. Время "varies based on duration, resolution, and current API load", конкретных цифр нет [?].
- Результат: base64 в ответе или `uri`; по `uri` нужно ждать состояния файла `ACTIVE` (опрос `files.get`, шаг 5 с в примере), потом скачивать с ключом. Файлы Files API живут **48 часов** [ОФ https://ai.google.dev/gemini-api/docs/files]. Сохранённые interactions: платный tier 55 дней (настраивается 7/14/28/55), бесплатный 1 день [ОФ https://ai.google.dev/gemini-api/docs/interactions-overview]. Замечание из доков: `GET /v1beta/interactions/{id}` отдаёт видео как inline base64, даже если создавалось с `delivery: uri`.

**Цена** [ОФ pricing]: tier бесплатный "Not available", платный: вход $1.50 за 1M токенов (text/image/video/audio), выход $9.00 за 1M токенов текста и **$17.50 за 1M токенов видео**; "5,792 tokens per second of 720p video ... approximately $0.10 per second". [РАСЧЁТ] 5 792 * 17.5e-6 = $0.1014/с.

| Разрешение | $/с | 3 с | 5 с | 8 с | 10 с | Статус |
|---|---|---|---|---|---|---|
| 720p | 0.101 | 0.30 | 0.51 | 0.81 | 1.01 | [ОФ]+[РАСЧЁТ] |
| 1080p | около 0.152 | 0.46 | 0.76 | 1.22 | 1.52 | вывод из $9.12/мин в таблице Artificial Analysis (в 1.5 раза от 720p), на странице цен Google не указано [ВТ] |
| 360p, 4k | не нашёл | | | | | [?] |

К этому добавляется оплата входных токенов (картинки, видео для правки). Поскольку длительность выбирает модель (3-10 с), точную цену до запроса показать нельзя, только диапазон; фактическая цена известна по токенам после генерации.

**Ограничения и водяной знак.** Все видео несут SynthID (невидимый) [ОФ]. Фильтры безопасности на вход и выход, зависят от региона. Загрузка и правка изображений с "certain recognizable people" не поддерживаются. Изображения с несовершеннолетними не поддерживаются в ЕЭЗ, Швейцарии, Великобритании. Полностью поддерживается английский, остальные языки не оценивались. C2PA и видимый водяной знак в доках не упомянуты.

### 4.3 Veo (в Gemini API закрыт, справка, потому что те же параметры у Veo на OpenRouter)

[ОФ] https://ai.google.dev/gemini-api/docs/veo (страница сохранена с пометкой о закрытии).

| Параметр | Veo 3.1 / 3.1 Fast | Veo 3.1 Lite |
|---|---|---|
| `durationSeconds` | `"4"`, `"6"`, `"8"`; строго 8 при 1080p, 4k, референсах и extension | то же без 4k |
| `resolution` | 720p (по умолчанию), 1080p (только 8 с), 4k (только 8 с); при extension только 720p | 720p, 1080p (только 8 с) |
| `aspectRatio` | 16:9, 9:16 | 16:9, 9:16 |
| `image`, `lastFrame` | да (последний кадр только вместе с первым) | да |
| `referenceImages` | до 3 | нет |
| `video` (extension) | да, видео, ранее созданное Veo; 7 с за шаг | нет |
| `personGeneration` | текст -> видео и extension: `allow_all`; картинка -> видео, интерполяция, референсы: `allow_adult` | то же |
| `negativePrompt`, `seed` | да (seed не гарантирует детерминизм) | да |
| Звук | всегда включён | всегда включён |
| fps | 24 | 24 |
| Видео в запросе | 1 | 1 |
| Задержка | от 11 с до 6 мин в часы пик | то же |
| Хранение | 2 дня | 2 дня |
| Водяной знак | SynthID | SynthID |

Заблокированное из-за аудио-фильтров видео не тарифицируется.

## 5. OpenRouter

Источники [ОФ]: https://openrouter.ai/docs/guides/overview/multimodal/video-generation (и `.md`), https://openrouter.ai/docs/api/api-reference/video-generation/submit-a-video-generation-request.md, `.../poll-video-generation-status.md`, `.../list-all-video-generation-models.md`, cookbook https://openrouter.ai/docs/cookbook/video-generation/*.md. Каталог [API]: https://openrouter.ai/api/v1/videos/models.

### 5.1 Протокол

1. `POST https://openrouter.ai/api/v1/videos` (Bearer-ключ) -> 202 `{id, polling_url, status: "pending", generation_id}`. `id` имеет вид `gen-vid-<unix>-<20 символов>`.
2. `GET /api/v1/videos/{id}` -> `status` из `pending`, `in_progress`, `completed`, `failed`, `cancelled`, `expired`. Процента готовности нет. Рекомендуемый интервал опроса в доке 30 с.
3. По `completed` приходят `unsigned_urls[]` (нужен ключ в `Authorization`, работают пока результат хранится) и, если у OpenRouter есть своя копия, `urls[]` (подписанные, без ключа) и `expires_at` (unix, когда копия исчезнет). Срок хранения числом не назван [?]. Скачать: `GET /api/v1/videos/{id}/content?index=0`. Параметр `index` нужен для моделей с несколькими выходами.
4. `usage.cost` (USD, фактическая стоимость) и `usage.is_byok`. По тексту страницы модели провалившиеся генерации не тарифицируются [ВТ: пересказ описания ошибки 502 со страницы модели].
5. Вебхук: `callback_url` в запросе (HTTPS) или общий в настройках workspace. События `video.generation.completed|failed|cancelled|expired`; заголовок `X-OpenRouter-Idempotency-Key: <job_id>-<status>`; подпись `X-OpenRouter-Signature: t=...,v1=...` (HMAC-SHA256 от `{t},{raw_body}`), если задан signing secret.
6. Типичное время: "30 seconds to several minutes depending on the model and parameters". Модельных цифр у OpenRouter нет [?].
7. Видео не подпадает под Zero Data Retention. Региональный EU-роутинг для видео недоступен. Оплата: цены провайдеров без наценки, комиссия только при пополнении кредитов; BYOK-ключи провайдеров тоже поддерживаются (5% сверх бесплатного лимита, лимит для pay-as-you-go $25 000/мес) [ОФ https://openrouter.ai/docs/faq]. Для видео задекларировать ZDR на ключе нельзя.

**Тело запроса `POST /api/v1/videos`** [ОФ]:

| Поле | Тип | Описание |
|---|---|---|
| `model` | string, обязательно | например `google/veo-3.1-lite` |
| `prompt` | string | обязателен, кроме моделей, умеющих работать только с картинкой |
| `duration` | integer, секунды | из `supported_durations` модели |
| `resolution` | string | `360p`, `480p`, `720p`, `768p`, `1080p`, `1K`, `2K`, `4K` |
| `aspect_ratio` | string | `16:9`, `9:16`, `1:1`, `4:3`, `3:4`, `3:2`, `2:3`, `21:9`, `9:21` |
| `size` | string `WxH` | заменяет `resolution`+`aspect_ratio` |
| `frame_images[]` | `{type:"image_url", image_url:{url}, frame_type:"first_frame"\|"last_frame"}` | image-to-video; URL https или data-URL |
| `input_references[]` | `image_url` \| `audio_url` \| `video_url` | референсы; аудио и видео учитывают только провайдеры с поддержкой (в доках названа BytePlus Seedance 2 и новее) |
| `generate_audio` | boolean | по умолчанию берётся из флага модели, "false if not set" |
| `seed` | integer | детерминизм не гарантирован |
| `previous_job_id` | string | продолжить или править завершённое задание "на той же модели и эндпоинте"; принимают только модели с поддержкой continuation, в каталоге это не помечено [?] |
| `callback_url` | https URL | вебхук |
| `provider.options.<slug>.parameters` | object | проброс параметров провайдера, только из `allowed_passthrough_parameters` модели |
| `upscale_factor`, `creativity` | | только для апскейлеров |

Если переданы и `frame_images`, и `input_references`, побеждает `frame_images`. Неверное значение даёт 400 со списком допустимых.

Не выставлено в нормализованной схеме: negative prompt, camera control, число вариантов (`n`), fps. Негатив и т. п. идёт только через passthrough отдельных моделей.

### 5.2 Каталог (все модели генерации из `/videos/models`) [API]

Значения `audio` и `seed`: "да" = флаг `true`, "нет" = `false`, "не указано" = `null`. Для `audio` по доке OpenRouter `null` означает по умолчанию `false`, но реальное поведение расходится с описаниями (Grok и HeyGen в описаниях заявляют звук), поэтому для таких моделей поведение надо проверять пробным запросом [?].

| id | эндпоинт | вход | длительность, с | разрешения | соотношения сторон | кадры | audio | seed | passthrough |
|---|---|---|---|---|---|---|---|---|---|
| `x-ai/grok-imagine-video-1.5-lite` | xAI | text+image | 1-15 | 480p, 720p, 1080p | 16:9, 9:16, 1:1, 4:3, 3:4, 3:2, 2:3 | first | не указано | не указано | - |
| `x-ai/grok-imagine-video` | xAI | text+image | 1-15 | 480p, 720p | 16:9, 9:16, 1:1, 4:3, 3:4, 3:2, 2:3 | first | не указано | не указано | - |
| `x-ai/grok-imagine-video-1.5` | xAI | text+image | 1-15 | 480p, 720p, 1080p | 16:9, 9:16, 1:1, 4:3, 3:4, 3:2, 2:3 | first | не указано | не указано | - |
| `heygen/heygen-video-1` | HeyGen | text+image+video+audio | 5-15 | 480p, 768p, 2K | 21:9, 16:9, 4:3, 1:1, 3:4, 9:16 | first | нет | да | prompt_enhancement |
| `minimax/hailuo-3-max` | MiniMax | text+image | 5-15 | 768p, 480p | 21:9, 16:9, 4:3, 1:1, 3:4, 9:16 | first,last | нет | нет | aigc_watermark |
| `minimax/hailuo-3` | MiniMax | text+image+video+audio | 5-15 | 2K | 21:9, 16:9, 4:3, 1:1, 3:4, 9:16 | first,last | да | нет | aigc_watermark |
| `minimax/hailuo-2.3` | MiniMax | text+image | 6,10 | 1080p | 16:9 | first | нет | не указано | prompt_optimizer, fast_pretreatment |
| `alibaba/wan-3.0` | Alibaba | text+image+video+audio | 2-30 | 480p, 720p, 1080p | 16:9, 4:3, 1:1, 3:4, 9:16 | first,last | да | да | - |
| `alibaba/wan-3.0-prime` | Alibaba | text+image+video+audio | 2-30 | 480p, 720p, 1080p | 16:9, 4:3, 1:1, 3:4, 9:16 | first,last | да | да | - |
| `alibaba/wan-2.7` | AtlasCloud | text+image | 2-10 | 720p, 1080p | 16:9, 9:16, 1:1, 4:3, 3:4 | first,last | да | да | negative_prompt, prompt_extend, audio, ratio, last_image, video, videos, images |
| `alibaba/wan-2.6` | AtlasCloud | text+image | 5,10 | 720p, 1080p | 16:9, 9:16 | first | да | да | negative_prompt, enable_prompt_expansion, shot_type, audio, size |
| `alibaba/happyhorse-1.1` | Alibaba | text+image | 3-15 | 720p, 1080p | 16:9, 9:16, 1:1, 4:3, 3:4, 21:9, 9:21 | first | не указано | да | - |
| `alibaba/happyhorse-1.0` | Alibaba | text+image | 3-15 | 720p, 1080p | 16:9, 9:16, 1:1, 4:3, 3:4, 21:9, 9:21 | first | не указано | да | - |
| `bytedance/seedance-2.5` | ByteDance (Seed) | text+image+video+audio | 4-30 | 480p, 720p | 16:9, 4:3, 1:1, 3:4, 9:16, 21:9 | first,last | да | да | watermark, req_key, output_format |
| `bytedance/seedance-2.0` | ByteDance (Seed) | text+image+video+audio | 4-15 | 480p, 720p, 1080p, 4K | 1:1, 3:4, 9:16, 4:3, 16:9, 21:9, 9:21 | first,last | да | да | watermark, req_key |
| `bytedance/seedance-2.0-fast` | ByteDance (Seed) | text+image+video+audio | 4-15 | 480p, 720p | 1:1, 3:4, 9:16, 4:3, 16:9, 21:9, 9:21 | first,last | да | да | watermark, req_key |
| `bytedance/seedance-2.0-mini` | ByteDance (Seed) | text+image+video+audio | 4-15 | 480p, 720p | 1:1, 3:4, 9:16, 4:3, 16:9, 21:9, 9:21 | first,last | да | да | watermark, req_key, return_last_frame |
| `bytedance/seedance-1-5-pro` | ByteDance (Seed) | text+image | 4-12 | 480p, 720p, 1080p | 1:1, 3:4, 9:16, 9:21, 4:3, 16:9, 21:9 | first,last | да | да | watermark, req_key |
| `kwaivgi/kling-v3.0-pro` | AtlasCloud | text+image | 3-15 | 720p | 16:9, 9:16, 1:1 | first,last | да | нет | negative_prompt, cfg_scale |
| `kwaivgi/kling-v3.0-std` | AtlasCloud | text+image | 3-15 | 720p | 16:9, 9:16, 1:1 | first,last | да | нет | negative_prompt, cfg_scale |
| `kwaivgi/kling-video-o1` | AtlasCloud | text+image | 5,10 | 720p | 16:9, 9:16, 1:1 | first,last | да | нет | negative_prompt |
| `google/veo-3.1` | Google (google-vertex) | text+image | 4,6,8 | 720p, 1080p, 4K | 16:9, 9:16 | first,last | да | да | personGeneration, aspectRatio, negativePrompt, conditioningScale, enhancePrompt |
| `google/veo-3.1-fast` | Google (google-vertex) | text+image | 4,6,8 | 720p, 1080p, 4K | 16:9, 9:16 | first,last | да | да | personGeneration, aspectRatio, negativePrompt, conditioningScale, enhancePrompt |
| `google/veo-3.1-lite` | Google (google-vertex) | text+image | 4,6,8 | 720p, 1080p | 16:9, 9:16 | first,last | да | да | personGeneration, aspectRatio, negativePrompt, conditioningScale, enhancePrompt |
| `runway/gen-4.5` | Runway | text+image | 2-10 | 720p | 16:9, 9:16 | first | нет | да | contentModeration |
| `black-forest-labs/flux-3-video` | Black Forest Labs | text+image+video | 5-20 | 720p, 1080p, 2K, 4K | 21:9, 16:9, 4:3, 1:1, 3:4, 9:16 | first,last | да | нет | safety_tolerance, version |

Эндпоинт каждой модели один (маршрутизации между провайдерами нет), кроме Kling v3/O1 и Wan 2.6/2.7, которые обслуживает посредник AtlasCloud. В живом срезе у `seedance-2.5`, `seedance-2.0-fast`, `veo-3.1-lite` статус эндпоинта отличался от 0 (смысл кодов не документирован), у остальных 0 [API].

**Особые модели (не "текст/картинка -> видео")** [API]:

| id | Что делает | Цена |
|---|---|---|
| `runway/aleph-2` | правка существующего видео по тексту и ключевым кадрам; passthrough `contentModeration`, `keyframes`; форматы 16:9...21:9; длительность и разрешения не заданы | 28 центов/с, минимум 56 центов за запрос |
| `black-forest-labs/flux-video-edit` | правка исходного видео (до 15 с, 50 МиБ; вход выше 720p сжимается до 720p; сохраняет длительность, формат и звук) | 3 цента/с |
| `black-forest-labs/flux-video-upscale` | апскейл видео в 1.5-3 раза, `creativity` 0-1 | 3.5 цента (precise) или 5 центов (creative) за мегапиксель-секунду |
| `heygen/avatar-iv` | фото + текст или аудио -> говорящая голова с lip-sync; passthrough `voice_id`, `voice_settings`, `motion_prompt`, `expressiveness`, `fit`, `remove_background`, `background`, `caption`, `title`; 720p/1080p; форматы 16:9, 9:16, 1:1 | $0.05/с |

### 5.3 Цены и примеры [РАСЧЁТ из `pricing_skus` [API]]

Правила чтения SKU:
- `cents_per_*` делить на 100; `duration_seconds*` уже в $/с;
- `video_tokens*` (Seedance): токены = ширина * высота * 24 * длительность / 1024 (формула в описании моделей на OpenRouter), цена = токены * ставка. Сторонняя ставка "with_video_input" применяется, когда на вход идёт видео. Счёт точнее (два платных прогона Seedance 2.0 Mini 31.10, совпало до токена): токены = ⌊(24·d + 1 + 24·max(сек. ролика, 3)) × W×H / 1024⌋, где W×H реальный размер выхода (у 2.0 при 480p 16:9 это 864×496, а не 854×480 из `supported_sizes`), ролик считается в размере выхода. Таблица размеров по разрешениям и пропорциям: https://docs.apiyi.com/en/api-capabilities/seedance2/overview.md (раздел "Resolutions & Aspect Ratios in Detail"); она в `price.sizes` оверлея;
- ключ без суффикса разрешения у Veo Fast/Lite трактую как 1080p: совпадает с таблицей Artificial Analysis ($0.12/с и $0.08/с на 1080p);
- на сайте OpenRouter у части моделей действует скидка, которой нет в `pricing_skus`: Wan 3.0 "15% off from $0.0425/second" при списочных 0.05, HeyGen Video 1 "50% off from $0.01/second" при списочных 0.02. Фактическую цену знает только `usage.cost` после задания. Цены меняются: в cookbook у Seedance 2.0 Fast ставка была $5.6/1M токенов, сейчас $4.2/1M.

Цена в USD (для 5 с и 8 с там, где длительность допустима; иначе указана ближайшая допустимая):

| Модель | Разрешение / режим | $/с | 5 с | 8 с |
|---|---|---|---|---|
| `x-ai/grok-imagine-video-1.5-lite` | 480p | 0.02 | 0.10 | 0.16 |
| | 720p | 0.03 | 0.15 | 0.24 |
| | 1080p (апскейл из 720p) | 0.14 | 0.70 | 1.12 |
| | входная картинка | +$0.01 за картинку | | |
| `x-ai/grok-imagine-video` (1.0) | 480p / 720p | 0.05 / 0.07 | 0.25 / 0.35 | 0.40 / 0.56 |
| `x-ai/grok-imagine-video-1.5` | 480p / 720p / 1080p | 0.08 / 0.14 / 0.25 | 0.40 / 0.70 / 1.25 | 0.64 / 1.12 / 2.00 |
| `heygen/heygen-video-1` (списочные) | 480p / 768p / 2K | 0.02 / 0.036 / 0.10 | 0.10 / 0.18 / 0.50 | 0.16 / 0.29 / 0.80 |
| | с референсами | 480p 0.07, 768p 0.10, 2K 0.18 за секунду + $0.04 за картинку | | |
| `minimax/hailuo-3-max` | 480p / 768p | 0.05 / 0.08 | 0.25 / 0.40 | 0.40 / 0.64 |
| `minimax/hailuo-3` | 2K | 0.13 (+$0.04 за референс-картинку) | 0.65 | 1.04 |
| `minimax/hailuo-2.3` | 1080p (6 или 10 с) | 0.0817 | 6 с: 0.49 | 10 с: 0.82 |
| `bytedance/seedance-2.0-mini` | 480p / 720p | 0.0336 / 0.0756 | 0.17 / 0.38 | 0.27 / 0.60 |
| `bytedance/seedance-2.0-fast` | 480p / 720p | 0.0404 / 0.0907 | 0.20 / 0.45 | 0.32 / 0.73 |
| `bytedance/seedance-2.0` | 480p / 720p / 1080p / 4K | 0.0673 / 0.1512 / 0.3742 / 0.7776 | 0.34 / 0.76 / 1.87 / 3.89 | 0.54 / 1.21 / 2.99 / 6.22 |
| `bytedance/seedance-2.5` | 480p / 720p | 0.1028 / 0.2311 | 0.51 / 1.16 | 0.82 / 1.85 |
| `bytedance/seedance-1-5-pro` (до 2026-11-11) | 480p / 720p / 1080p, со звуком | 0.0231 / 0.0518 / 0.1166 | 0.12 / 0.26 / 0.58 | 0.18 / 0.41 / 0.93 |
| | то же без звука (вдвое дешевле) | 0.0115 / 0.0259 / 0.0583 | 0.06 / 0.13 / 0.29 | 0.09 / 0.21 / 0.47 |
| `alibaba/wan-3.0` (списочные) | 480p / 720p / 1080p | 0.05 / 0.10 / 0.20 | 0.25 / 0.50 / 1.00 | 0.40 / 0.80 / 1.60 |
| `alibaba/wan-3.0-prime` | 480p / 720p / 1080p | 0.068 / 0.14 / 0.28 | 0.34 / 0.70 / 1.40 | 0.54 / 1.12 / 2.24 |
| `alibaba/wan-2.7` | 720p и 1080p | 0.10 | 0.50 | 0.80 |
| `alibaba/wan-2.6` (только 5 и 10 с) | t2v 720p / 1080p | 0.08 / 0.12 | 0.40 / 0.60 | 10 с: 0.80 / 1.20 |
| | i2v 720p / 1080p | 0.10 / 0.15 | 0.50 / 0.75 | 10 с: 1.00 / 1.50 |
| `alibaba/happyhorse-1.1` | 720p / 1080p | 0.0988 / 0.1278 | 0.49 / 0.64 | 0.79 / 1.02 |
| `alibaba/happyhorse-1.0` | 720p / 1080p | 0.0988 / 0.1694 | 0.49 / 0.85 | 0.79 / 1.36 |
| `kwaivgi/kling-v3.0-std` | 720p без звука / со звуком | 0.084 / 0.126 | 0.42 / 0.63 | 0.67 / 1.01 |
| `kwaivgi/kling-v3.0-pro` | 720p без звука / со звуком | 0.112 / 0.168 | 0.56 / 0.84 | 0.90 / 1.34 |
| `kwaivgi/kling-video-o1` (5 и 10 с) | 720p | 0.112 | 0.56 | 10 с: 1.12 |
| `runway/gen-4.5` | 720p | 0.12 | 0.60 | 0.96 |
| `black-forest-labs/flux-3-video` | 720p / 1080p / 2K / 4K | 0.17 / 0.29 / 0.28 / 0.41 | 0.85 / 1.45 / 1.40 / 2.05 | 1.36 / 2.32 / 2.24 / 3.28 |
| | продолжение видео | 720p 0.41, 1080p 0.53, 2K 0.31, 4K 0.45 за секунду | | |
| `google/veo-3.1-lite` (4 с и 8 с) | 720p со звуком / без | 0.05 / 0.03 | 4 с: 0.20 / 0.12 | 8 с: 0.40 / 0.24 |
| | 1080p со звуком / без (только 8 с) | 0.08 / 0.05 | | 8 с: 0.64 / 0.40 |
| `google/veo-3.1-fast` (4 с и 8 с) | 720p со звуком / без | 0.10 / 0.08 | 4 с: 0.40 / 0.32 | 8 с: 0.80 / 0.64 |
| | 1080p со звуком / без | 0.12 / 0.10 | | 8 с: 0.96 / 0.80 |
| | 4K со звуком / без | 0.30 / 0.25 | | 8 с: 2.40 / 2.00 |
| `google/veo-3.1` (4 с и 8 с) | 720p и 1080p со звуком / без | 0.40 / 0.20 | 4 с: 1.60 / 0.80 | 8 с: 3.20 / 1.60 |
| | 4K со звуком / без | 0.60 / 0.40 | | 8 с: 4.80 / 3.20 |
| `gemini-omni-1.1-flash` (Gemini API, для сравнения) | 720p | 0.101 | 0.51 | 0.81 (длительность выбирает модель) |

Зависимость цены от звука: есть у Veo (звук дороже в 1.25-2 раза), у Kling v3 (звук +50%), у Seedance 1.5 Pro (звук x2). Нет у Seedance 2.x (ставка одна), Wan 3.0, Omni, Grok.

Оговорки: у Kling в метаданных `supported_resolutions` только `720p`, хотя в SKU есть 480p/1080p. У `wan-2.6` метаданные дают 720p/1080p, хотя в SKU есть 480p. Верить метаданным допустимых значений, SKU использовать только как таблицу ставок.

### 5.4 Режимы и особенности по моделям

Колонка "кадры" в таблице 5.2 показывает только `first`/`last`. Остальное по описаниям моделей на OpenRouter [API/страницы моделей] и документации вендоров:

| Режим | Какие модели |
|---|---|
| Только первый кадр | Grok Imagine (все три), Runway Gen-4.5, HeyGen Video 1, HappyHorse, Wan 2.6, Hailuo 2.3 |
| Первый + последний кадр | Veo 3.1/Fast/Lite, Kling v3/O1, Seedance (все), Wan 3.0/Prime/2.7, Hailuo 3/3-Max, FLUX 3 Video |
| Референс-картинки (`input_references`) | Seedance 2.0/Fast/Mini/2.5 (картинки, видео, аудио; у 2.5 в описании "up to 50 image, video, and audio reference assets"), Wan 3.0 и 2.7 (reference-to-video), HappyHorse (набор референсов), Hailuo 3 (SKU за референс-картинку), HeyGen Video 1 (картинки, видео, аудио), Veo 3.1 и Fast (до 3, по доке Google; в Lite нет), Grok Imagine 1.0 (в описании до 7 референсов; xAI в своей документации называет до 14 картинок и 3 голосовых референса для 1.5, но метаданные OpenRouter дают у Grok только `first_frame`, так что через OpenRouter референсы Grok могут быть недоступны [?]) |
| Правка видео / продление | Seedance 2.5 ("video editing, and video extension"), FLUX 3 Video ("video continuation", отдельные SKU), Hailuo 3 (инструкции-правки, перенос движения), Veo 3.1 ("scene extension up to 20 chained clips" в описании OpenRouter; реализуется ли это через `previous_job_id`, не проверено [?]), отдельные Runway Aleph 2 и FLUX Video Edit, Gemini Omni (Interactions API) |
| Negative prompt | Veo (`negativePrompt`), Kling v3/O1 (`negative_prompt`; у v3 ещё `cfg_scale`, диапазон не указан [?]), Wan 2.6/2.7 (`negative_prompt`) |
| Улучшение промпта | Veo `enhancePrompt`, Wan 2.7 `prompt_extend`, Wan 2.6 `enable_prompt_expansion`, HeyGen `prompt_enhancement`, Hailuo 2.3 `prompt_optimizer` |
| Многокадровость/камера | Wan 2.6 `shot_type` (значения не проверены [?]); Seedance 1.5 Pro заявляет камеру промптом (pan, tilt, zoom, orbit); параметров камеры в API нет ни у одной модели |
| Видимый водяной знак (опция) | Seedance `watermark`, Hailuo 3/3-Max `aigc_watermark`; значения по умолчанию не проверены [?] |
| Лица людей в `personGeneration` | только Veo: `allow_adult` для картинка->видео, `allow_all` для текста [ОФ, Google] |

## 6. Ограничения контента и водяные знаки

| Провайдер / модель | Что известно | Статус |
|---|---|---|
| Veo | SynthID во всех видео; `personGeneration` (см. 4.3); в ЕС, Великобритании, Швейцарии, MENA для текст->видео допускается только `allow_adult` | [ОФ Google] |
| Gemini Omni | SynthID; нераспознаваемые "certain recognizable people" нельзя загружать и править; несовершеннолетние на фото запрещены в ЕЭЗ/CH/UK; правка и продление чужих видео не работают в ЕЭЗ/CH/UK | [ОФ Google] |
| Sora (закрыта) | лица реальных людей на входе отклонялись | [ОФ OpenAI] |
| Seedance 2.x | реальные лица в референсах отклоняются на уровне входа; обход через "asset library" с проверкой живости или готовые виртуальные портреты BytePlus | [ВТ] (Segmind, apiyi, aividpipeline; официальной страницы не получил) |
| Kling, Wan, HappyHorse, Grok, Runway, FLUX, HeyGen, Hailuo | политика по лицам и водяным знакам не проверена; у Runway есть passthrough `contentModeration`, у FLUX `safety_tolerance` | [?] |
| C2PA | ни на одной прочитанной официальной странице не упомянут | не нашёл |

Практический вывод: для бота, куда пользователи кидают селфи, надо до запуска проверить каждую выбранную модель на фото с лицом, а в интерфейсе заранее предупреждать, какие модели такие фото не принимают.

## 7. Качество и кандидаты на бесплатный тариф

Качество: слепой Elo-рейтинг Artificial Analysis, страницы `https://artificialanalysis.ai/video/leaderboard/text-to-video` и `.../image-to-video` (основные таблицы по умолчанию (с аудио), сняты 2026-10-26; оценка делалась на "default settings", цена у них за 1080p; какое разрешение использовалось в замерах конкретной модели, не указано) [ВТ, независимый рейтинг, не вендор]. Высокий Elo при низком разрешении не гарантируется.

| Модель на OpenRouter | T2V Elo (место из 30) | I2V Elo | Конфигурация для free | Цена за ролик (списочная) | Кадры | Замечания |
|---|---|---|---|---|---|---|
| `heygen/heygen-video-1` | 1157 (4); в рейтинге без звука 1143 (1) | в таблице нет | 768p, 5 с | $0.18 (с акцией на сайте OpenRouter около $0.09) | first | релиз 2026-09-30; флаг audio=false при описании со звуком; позиционируется для бизнес-роликов, не аватар |
| `minimax/hailuo-3-max` | 1152 (7) | 1125 (6) | 480p 5 с / 768p 5 с | $0.25 / $0.40 | first, last | audio=false |
| `alibaba/wan-3.0` | 1174 (1) | 1127 (5) | 480p, 5 с | $0.25 (на сайте -15%) | first, last, референсы | длительность 2-30 с |
| `x-ai/grok-imagine-video-1.5-lite` | 990 (19) | в таблице нет | 720p, 5 с | $0.15 (+$0.01 за входное фото) | first | цена подтверждена xAI; 1080p это апскейл 720p; поведение звука не указано |
| `google/veo-3.1-lite` | 951 (23) | в таблице нет | 720p, 4 с, со звуком | $0.20 | first, last | SynthID, единственная с документированным `allow_adult` для лиц |
| `bytedance/seedance-2.0-mini` | в таблице нет (у Seedance 2.0: 1136) | нет | 480p 5 с / 720p 5 с | $0.17 / $0.38 | first, last, референсы | звук, референсы картинок/видео/аудио |
| `bytedance/seedance-1-5-pro` | в таблице нет | нет | 480p, 5 с, без звука | $0.06 | first, last | убирают с OpenRouter 2026-11-11, для планирования непригодна |
| `gemini-omni-1.1-flash` (Gemini API) | 1128 (10) | 1132 (4) | 720p | $0.30-1.01 (длительность выбирает модель) | first, last, референсы | нет бесплатного tier |

## 8. Выводы для framy

### 8.1 Что выбрать для бесплатного "одно видео в неделю"

Платит framy своим ключом OpenRouter, поэтому ограничивающий параметр это цена ролика, умноженная на число пользователей. Все модели ниже в одном API, переключение сводится к строке `model`.

Варианты и моё решение:

1. **`heygen/heygen-video-1`, 768p, 5 с, около $0.18.** Лучшее отношение рейтинга к цене из проверенного: Elo 1157 при цене уровня Grok Lite. Минусы: модель вышла месяц назад, акция -50% временная (после неё всё равно $0.18), в метаданных audio=false, хотя описание обещает звук, поддерживает только первый кадр. **Мой выбор как основной**, при условии пробного прогона.
2. **`x-ai/grok-imagine-video-1.5-lite`, 720p, 5 с, $0.15.** Самая дешёвая из заслуживающих внимания, цена подтверждена официальной страницей xAI. Рейтинг ниже (990), но выше всего семейства Veo 3.1. **Мой выбор как резервный** (вторая модель при сбое или конце акции).
3. **`minimax/hailuo-3-max`, 480p, 5 с, $0.25**, с последним кадром, Elo 1152. Пригоден как третья в очереди: 2.5 раза дороже Grok Lite на 480p/5 с ($0.25 против $0.10), но принимает first+last.
   **`alibaba/wan-3.0`, 480p, 5 с, $0.25** (на сайте с акцией около $0.21): первое место в T2V-рейтинге, first+last, референсы, звук по умолчанию, длительность 2-30 с. Равноценная альтернатива Hailuo 3 Max; на 480p картинка заметно проще, чем в замерах AA, которые идут по 1080p.
4. `google/veo-3.1-lite`, 720p, 4 с, $0.20. Самый предсказуемый по политике лиц и SynthID, но по рейтингу ниже первых трёх вариантов. Подходит, если юридическая чистота важнее качества.
5. `bytedance/seedance-2.0-mini`, 480p, $0.17: дёшево и с референсами, но данных о качестве нет; не выбирал бы без собственного теста.
6. Gemini Omni в бесплатный тариф не подходит: на Gemini API нет free tier, цена $0.30-1.01 за ролик, длительность не фиксируется.

Перед выбором: слепой прогон 10 одинаковых промптов (5 текстовых, 5 с фото, часть с лицами) на 4 кандидатах, около $5-8 в сумме. Рейтинг AA не заменяет проверки на ваших типичных запросах.

Ограничения тарифа, которые держат стоимость: фиксированные 5 с, максимум 768p, без референсов и продления, глобальный недельный бюджет с отключением бесплатной генерации при превышении.

### 8.2 Единый набор параметров интерфейса

| Поле framy | OpenRouter | Gemini Omni | Условие показа |
|---|---|---|---|
| Модель | `model` | `model` | всегда |
| Промпт | `prompt` | `input` (text) | всегда |
| Негативный промпт | passthrough (`negativePrompt` у Veo, `negative_prompt` у Kling и Wan 2.6/2.7) | нет, текстом в промпт | только у моделей с passthrough |
| Начальный кадр | `frame_images` + `frame_type: first_frame` | image + `<FIRST_FRAME>` | `supported_frame_images` содержит `first_frame` (Omni: всегда) |
| Конечный кадр | `frame_type: last_frame` | вторая image + `<LAST_FRAME>` | содержит `last_frame` |
| Референсы (картинки / видео / аудио) | `input_references` | `<IMAGE_REF_N>`, `<VIDEO_REF_N>` | по таблице особенностей (в API числа не отдаются) |
| Длительность | `duration` | не задаётся (3-10 с) | дискретный набор или диапазон из `supported_durations` |
| Разрешение | `resolution` или `size` | `response_format.resolution` | из `supported_resolutions` |
| Соотношение сторон | `aspect_ratio` | `response_format.aspect_ratio` (16:9, 9:16) | из `supported_aspect_ratios` |
| Звук | `generate_audio` | только промптом | флаг `generate_audio` не `false`; рядом показывать приращение цены (Veo, Kling, Seedance 1.5) |
| Seed | `seed` | нет | флаг `seed` равен `true` |
| Продолжить / изменить результат | `previous_job_id` | `previous_interaction_id` | кнопка на карточке готового видео, только у подтверждённых моделей |
| Вебхук | `callback_url` | `webhook_config` | серверная часть |

Разрешения и соотношения сторон разных моделей не совпадают (480p/768p/2K/4K, 21:9 и 9:21 есть не везде). Интерфейс обязан показывать только допустимые значения выбранной модели и при смене модели сбрасывать недопустимые.

### 8.3 Что уникально и требует отдельного элемента

- Veo: звук всегда включён у Google, но у OpenRouter с ценовым переключателем; 1080p и 4K только на 8 с; `personGeneration`; референсы до 3 (не в Lite); последний кадр только вместе с первым.
- Gemini Omni: диалоговая правка через `previous_interaction_id`; длительность нельзя выбрать; тег-синтаксис ролей кадров; видео-референсы; продление до 40 с суммарно; цена зависит от фактической длины.
- Seedance 2.x/2.5: референсы видео и аудио, правка и продление (2.5), 4K у 2.0; оплата по токенам, цена растёт при видео на входе; видимый `watermark`.
- Kling v3: `negative_prompt`, `cfg_scale`; 720p; звук +50% цены.
- Wan 3.0: длительность 2-30 с; Seedance 2.5: 4-30 с; FLUX 3 Video: 5-20 с и отдельная цена продления.
- Hailuo 3: референс-картинки с отдельной ценой, перенос движения с видео.
- Runway Aleph 2, FLUX Video Edit/Upscale, HeyGen Avatar IV: другие входы (видео, аудио); в основной форме "создать видео" их не показывать, а вынести в отдельные действия "изменить / улучшить / оживить фото с голосом".

### 8.4 Как строить интерфейс и цену

1. **Каталог из API, не из кода.** Раз в час тянуть `GET /api/v1/videos/models` (без ключа), кэшировать, строить из него селекторы длительности/разрешения/формата/кадров. Дополнять локальным файлом (YAML) с тем, чего в API нет: число референсов по типам, подтверждённая поддержка `previous_job_id`, passthrough-параметры с допустимыми значениями, политика лиц, статус "рекомендована для free". Следить за `expiration_date` и убирать модели заранее.
2. **Цена как функция.** Для каждой модели хранить тип тарификации (секунды × ставка по разрешению; секунды × ставка со звуком/без; токены по формуле Seedance; Omni по токенам с диапазоном 3-10 с; за входные картинки). Показывать до запуска "около $X" с пометкой, что списочная цена выше возможной акции, а после задания записывать `usage.cost`. Для Omni и моделей с видео на входе показывать "до $X".
3. **Результат копировать сразу** в своё хранилище и отдавать пользователю свою ссылку. Причины: ссылка Sora жила час, Files API у Google 48 часов, у OpenRouter срок хранения не назван, `unsigned_urls` требуют ключ, а `urls` подписаны и считаются секретом.
4. **Ключи пользователя**: OpenRouter-ключ даёт всё; ключ Gemini даёт только Omni и только на платном tier (подсказать пользователю заранее); ключ OpenAI видео не даёт, в интерфейсе провайдер OpenAI для видео не показывать. Все запросы лучше слать с сервера framy (ключ шифровать), вебхук приходит на сервер.
5. **Ошибки**: `failed` с текстом (например, нарушение политики контента) показать как есть и объяснить, что платежа не было (у Veo и OpenRouter сбойные генерации не тарифицируются, подтверждено частично, см. разделы 4.3 и 5.1).

### 8.5 Как показывать ожидание

- Состояния: "в очереди" (`pending`), "генерируется" (`in_progress`), "готово", "ошибка", "отменено", "истекло". Процента готовности нет ни у OpenRouter, ни у Omni, поэтому показывать неопределённый индикатор и таймер "прошло 1:20", а не процент.
- Ожидаемое время: OpenRouter говорит "30 секунд - несколько минут", Veo 11 с - 6 мин (Google), у Sora было "several minutes". Свои медианы по моделям и разрешениям собирать с первых запусков и показывать "обычно 1-3 минуты" из них. Длинные ролики и 1080p/4K дольше.
- Опрос сервером раз в 10-30 с (OpenAI советовал 10-20 с, OpenRouter 30 с); клиенту отдавать состояние через SSE или WebSocket, а не заставлять опрашивать самому.
- Задание не привязано к открытой вкладке. Сайт и Mini App показывают историю заданий, Telegram-бот присылает готовое видео сообщением. Для этого использовать `callback_url` (OpenRouter) или `webhook_config` (Gemini), а опрос оставить запасным путём.
- В Telegram видео отправляется ботом: проверять размер файла против лимита Bot API (у вас есть свой telegram-bot-api на ws04, там лимит выше).
- До запуска показывать цену и ожидаемое время рядом с кнопкой "Создать", после запуска цену не менять.

## 9. Что не проверено или рискованно

- Единицы `pricing_skus` выведены, не задокументированы; при смене формата OpenRouter расчёты сломаются. Страховка: сверять оценку с `usage.cost` и поднимать тревогу при расхождении больше 20%.
- Какие модели принимают `previous_job_id` (правка/продление через OpenRouter): в каталоге флага нет. Нужен пробный запрос на Seedance 2.5, FLUX 3 Video, Veo 3.1.
- Поведение звука у Grok, HeyGen Video 1, HappyHorse (флаг `null` или `false` против описаний).
- Допустимые значения `response_format.duration` в Gemini Omni.
- Асинхронный `background=true` для Omni (документация перечисляет другие модели).
- Политики по лицам для всех моделей, кроме Veo, Omni, Sora; водяные знаки Kling, Wan, Grok, Runway, FLUX, HeyGen; C2PA нигде.
- Время генерации по моделям: у провайдеров нет цифр.
- Точная стоимость Omni при 360p, 1080p и 4K: на странице Google есть только 720p.
- Рейтинг AA не отражает ваш сценарий (фото пользователей, мемы); нужен собственный слепой тест.
- Сайт OpenRouter показывает скидки (Wan 3.0 -15%, HeyGen -50%), которые могут закончиться.

## 10. Источники

Официальные:
- OpenAI: https://developers.openai.com/api/docs/guides/video-generation, https://developers.openai.com/api/docs/deprecations, https://developers.openai.com/api/docs/changelog
- Google: https://ai.google.dev/gemini-api/docs/video, https://ai.google.dev/gemini-api/docs/omni, https://ai.google.dev/gemini-api/docs/models/gemini-omni-flash, https://ai.google.dev/gemini-api/docs/veo, https://ai.google.dev/gemini-api/docs/pricing, https://ai.google.dev/gemini-api/docs/deprecations, https://ai.google.dev/gemini-api/docs/changelog, https://ai.google.dev/api/interactions-api, https://ai.google.dev/gemini-api/docs/interactions-overview, https://ai.google.dev/gemini-api/docs/files
- OpenRouter: https://openrouter.ai/api/v1/videos/models, https://openrouter.ai/api/v1/models?output_modalities=video, https://openrouter.ai/docs/guides/overview/multimodal/video-generation, https://openrouter.ai/docs/api/api-reference/video-generation/submit-a-video-generation-request, https://openrouter.ai/docs/cookbook/video-generation/choose-video-model, https://openrouter.ai/docs/faq, страницы моделей `https://openrouter.ai/<id>`
- xAI: https://docs.x.ai/docs/models (цены Imagine), https://docs.x.ai/docs/guides/video-generation

Вторичные:
- Artificial Analysis: https://artificialanalysis.ai/video/leaderboard/text-to-video, https://artificialanalysis.ai/video/leaderboard/image-to-video
- Политика лиц Seedance: https://docs.apiyi.com/en/faq/seedance2-asset-face-reference, https://blog.segmind.com/seedance-2-0-error-guide-every-error-explained-with-fixes/, https://aividpipeline.com/blog/seedance-real-human-face-rules-2026
- Формула токенов Seedance и цены BytePlus (сопоставление с OpenRouter): https://blog.segmind.com/seedance-2-vs-seedance-2-fast-how-video-token-pricing-actually-works, страница цен BytePlus ModelArk (SPA, содержимого не получил)
- Закрытие Sora: https://help.make.com/openai-sora-and-video-modules-deprecation-by-september-24-2026
