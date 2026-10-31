# Модели генерации и редактирования картинок для framy (OpenAI, Gemini API, OpenRouter)

Дата сбора данных: 2026-10-26. Всё ниже получено сегодня из живых источников, знания из памяти не использовались, кроме пунктов с пометкой [?].

## 0. Как читать пометки достоверности

| Пометка | Значение |
|---|---|
| [О] | Прочитано сегодня на официальной странице вендора (URL в разделе 12) |
| [OR] | Взято сегодня из живого API OpenRouter (`/api/v1/images/models`, `/endpoints`, `/api/v1/models`) или со страницы модели на openrouter.ai. Для цен и параметров OpenRouter это первичный источник. Для фактов о самой модели (скорость, качество) это пересказ вендора |
| [В] | Вторичный источник (новости, агрегаторы, форумы, Artificial Analysis). Использовать с осторожностью |
| [?] | Не нашёл или не смог проверить |

## 1. Главное, что изменилось

1. OpenAI: актуальны только `gpt-image-2.5-sunburst` и `gpt-image-2.5-flare` (выпуск 2026-09-08) [О]. `gpt-image-2` ещё работает. DALL-E 2/3 выключены 2026-05-12, `gpt-image-1` выключен 2026-10-23, `gpt-image-1-mini`, `gpt-image-1.5` и `chatgpt-image-latest` выключаются 2026-12-01 [О, deprecations]. OpenRouter всё ещё показывает `openai/gpt-image-1` и `gpt-image-1-mini` [OR]: не включать в каталог.
2. Google: `gemini-nano-banana-2.1` (выпуск 2026-10-06, рекомендуется для новых проектов), `gemini-3.1-flash-image` (Nano Banana 2, "предыдущее поколение"), `gemini-3.1-flash-lite-image` (Nano Banana 2 Lite), `gemini-3-pro-image` (Nano Banana Pro), `gemini-2.5-flash-image` (легаси) [О]. Imagen выключен 2026-08-17, в Gemini API его больше нет [О].
3. Gemini API: рекомендуемый интерфейс теперь Interactions API (`POST /v1beta/interactions`, `response_format.aspect_ratio` / `image_size`, `generation_config.thinking_level`). `generateContent` назван легаси, но "остаётся полностью поддерживаемым"; Batch API пока только у `generateContent` [О].
4. Gemini API: free tier для всех картиночных моделей "Not available" [О, pricing]. Ключ Google AI Studio пользователя должен быть на проекте с включённым биллингом.
5. OpenRouter получил отдельный Images API: `POST /api/v1/images`, каталог `GET /api/v1/images/models` (61 модель) и записи по эндпоинтам с ценой и точными допустимыми значениями параметров [OR]. Старый путь `POST /chat/completions` с `modalities: ["image","text"]` и `image_config` тоже остаётся. Бесплатных (нулевая цена) картиночных моделей в списке нет [OR].
6. Картиночных моделей на OpenRouter много: FLUX.3 и FLUX.2, Seedream 5.0, Qwen Image 3, Krea 2 и K3 Edit, Recraft V4.1, Riverflow V2.5, Grok Imagine, MAI-Image-2.6, Tencent Hy Image 3.5 (это Hunyuan), Meta Muse Image.

## 2. Как устроены три провайдера (механика API)

### 2.1 OpenAI (прямой API)

Эндпоинты [О]: `POST /v1/images/generations`, `POST /v1/images/edits`, плюс инструмент `image_generation` в Responses API (`action`: `auto|generate|edit`, `input_image_mask {file_id}`, `previous_response_id` для многошагового редактирования; добавляются токены основной модели).

| Параметр | Значения | Заметки |
|---|---|---|
| `model` | `gpt-image-2.5-sunburst`, `gpt-image-2.5-flare`, `gpt-image-2` (+ снапшоты `-2026-09-08`, `-2026-04-21`) | |
| `prompt` | до 32 000 символов | |
| `n` | 1..10 | цена умножается на n |
| `size` | `auto`, `1024x1024`, `1536x1024`, `1024x1536` или любое `WxH` | для gpt-image-2 и 2.5: обе стороны кратны 16, длинная/короткая не больше 3:1, максимум 3840 px по стороне, суммарно от 655 360 до 8 294 400 px; выше 2560x1440 экспериментально |
| `quality` | `auto` (по умолчанию), `low`, `medium`, `high`; у `gpt-image-2.5-*` ещё `xhigh`, `max` | у `gpt-image-2` только low/medium/high |
| `background` | `transparent`, `opaque`, `auto` | прозрачность требует png или webp. У `gpt-image-2` в preview, у обоих 2.5 поддерживается |
| `output_format` | `png` (по умолчанию), `jpeg`, `webp` | |
| `output_compression` | 0..100 | только jpeg и webp |
| `moderation` | `auto` (по умолчанию), `low` | `low` мягче |
| `stream`, `partial_images` | `partial_images` 0..3 | каждое частичное изображение +100 выходных токенов |
| `safety_identifier` | до 128 символов | хэш id пользователя; `user` устарел |
| `input_fidelity` | `high`/`low` | для gpt-image-2 и 2.5 не передавать (вход всегда в высокой точности, из-за этого вход дороже) |
| `style`, `response_format` | устарели | для GPT image моделей не поддерживаются; ответ всегда base64 |

Edit [О]: `image` до 16 файлов (PNG, WebP, JPG, каждый менее 50 МБ); `mask` PNG менее 4 МБ тех же размеров, прозрачные области = где править; при нескольких картинках маска применяется к первой; маска работает "по подсказке", форма может не соблюдаться строго.

Лимиты по умолчанию (tier Build): 250 000 TPM и 20 IPM; Launch 150 IPM; Grow 250 IPM [О]. Для общего ключа framy на бесплатном тарифе 20 картинок в минуту мало, нужен рост tier'а.

Возможное требование Verified Organization для gpt-image [В, форумы: было для gpt-image-1 и gpt-image-2]; про 2.5 официального текста не нашёл [?]. Для BYOK-пользователей это возможный барьер (ошибка 403).

### 2.2 Gemini API (прямой)

Interactions API [О]: `POST https://generativelanguage.googleapis.com/v1beta/interactions`, заголовок `x-goog-api-key`.

| Поле | Значения |
|---|---|
| `model` | `gemini-nano-banana-2.1`, `gemini-3.1-flash-image`, `gemini-3.1-flash-lite-image`, `gemini-3-pro-image`, `gemini-2.5-flash-image` |
| `input` | строка или массив блоков `{"type":"text","text":...}`, `{"type":"image","mime_type":...,"data":<base64>}`, видео (`uri`) |
| `response_format` | `{"type":"image","mime_type":"image/png"|"image/jpeg","aspect_ratio":"16:9","image_size":"2K"}`; text+image: массив `[{"type":"text"},{"type":"image"}]` |
| `image_size` | `512px` (только 3.1 Flash Image), `1K`, `2K`, `4K`; заглавная K обязательна, "1k" отклоняется; по умолчанию 1K |
| `aspect_ratio` | по умолчанию как у входной картинки, иначе 1:1; значения в разделе 5 и 4.2 |
| `generation_config.thinking_level` | `minimal`, `medium`, `high` у Nano Banana 2.1 (по умолчанию `medium`); `minimal`, `high` у 3.1 Flash Image и Lite (по умолчанию `minimal`) |
| `tools` | `[{"type":"google_search"}]`, опционально `"search_types":["web_search","image_search"]` |
| `previous_interaction_id` | многошаговое редактирование |
| `store` | `false` отключает серверное хранение |

Особенности [О]:
- Размер в пикселях задать нельзя, только `image_size` + `aspect_ratio` (таблицы пикселей в 4.2).
- Параметра `n` нет: модель "не всегда следует точному числу запрошенных картинок". Варианты = несколько запросов, цена умножается.
- Параметров `quality`, `seed`, `negative_prompt`, `style`, `mask` в документации нет. Редактирование текстом. В описании OpenRouter для Nano Banana 2.1 заявлено "mask- and ink-based editing" [OR], механизм в документации Google не описан [?].
- Thinking включён всегда; мысли-черновики (до двух картинок) не тарифицируются, мыслительные токены тарифицируются.
- Все изображения содержат SynthID (у Lite ещё C2PA).
- Поиск-граундинг: 5 000 бесплатных запросов в месяц (общий лимит для Gemini 3+), потом $14 за 1 000. Google Search поддерживают 2.1, 3.1 Flash и Pro; image search только 2.1 и 3.1 Flash; Lite граундинг не поддерживает.
- Lite: до 14 референсов, но Google пишет "не оптимизирована для множества референсов и многошагового редактирования".
- Старый `generateContent`: поля `generationConfig.responseModalities`, `imageConfig.aspectRatio/imageSize`, `thinkingConfig` [?] (из памяти, сегодня на странице не перепроверялись).
- Лимиты запросов для картиночных моделей считаются в IPM и TPM [О, rate-limits]; значения по tier'ам не извлекал [?].

### 2.3 OpenRouter

Images API [OR]: `POST https://openrouter.ai/api/v1/images`. Поля: `model`, `prompt`, `n` (1..10, реальный потолок у каждой модели свой), `resolution` (`512|768|1K|1.5K|2K|4K`), `aspect_ratio` (`auto` + набор значений), `size` (уровень или пиксели вроде `2048x2048`, перекрывает остальное; при конфликте 400), `quality` (`auto|low|medium|high`; у OpenAI 2.5 ещё `xhigh|max`), `output_format` (`png|jpeg|webp|svg`), `background` (`auto|transparent|opaque`), `output_compression` (0..100), `seed`, `stream`, `input_references` (массив `{"type":"image_url","image_url":{"url":"https://... или data:..."}}`), `user`, блок `provider` (`only|order|ignore|sort|allow_fallbacks|options`).

Допустимые значения для каждой модели в `supported_parameters` с типами `enum`, `range`, `boolean`. Ответ: `data[].b64_json`, `media_type`, `usage.cost` в USD.

Чего в Images API нет: маски, negative prompt, `thinking_level` для Gemini. Специфика передаётся через `provider.options.<provider_slug>` с ключами из `allowed_passthrough_parameters` (например FLUX.2: `safety_tolerance`, а у Flex ещё `steps` и `guidance`).

Биллинг [OR]: "всё или ничего", неудавшаяся генерация даёт 502 и не списывается. Цены равны прейскуранту провайдера без наценки; 5,5% комиссии берётся при покупке кредитов (Standard) [OR, /pricing]. Лимит тела запроса: 413 при превышении, число не указано; у Sourceful жёсткие 4,5 МБ, лучше передавать URL, а не base64.

Через chat: `POST /chat/completions` с `modalities: ["image","text"]` и `image_config` (ключи зависят от модели). `meta/muse-image` доступна только так.

## 3. Модели OpenAI

| Модель | Статус | Что умеет | Цена |
|---|---|---|---|
| `gpt-image-2.5-sunburst` | актуальна, снапшот 2026-09-08 | T2I, edit, до 16 референсов, маска, прозрачный фон, quality до `max`, n до 10, стрим | токены: вход картинок $8/М (кэш $2/М), выход $30/М, вход текста $5/М (кэш $1,25/М); batch выход $15/М [О] |
| `gpt-image-2.5-flare` | актуальна, снапшот 2026-09-08 | то же, быстрая версия | те же тарифы [О] |
| `gpt-image-2` | актуальна, снапшот 2026-04-21 | T2I, edit, 16 референсов, маска, quality low/medium/high, прозрачность в preview | те же тарифы [О] |
| `gpt-image-1.5` | выключение 2026-12-01 | T2I, edit | выход $32/М, вход картинок $8/М [О] |
| `gpt-image-1-mini` | выключение 2026-12-01 | 3 фиксированных размера | выход $8/М, вход картинок $2,5/М [О] |
| `gpt-image-1` | выключена 2026-10-23 | - | не использовать |

Позиционирование [О, image-prompting]: Sunburst это базовая модель, качество выше GPT Image 2; Flare это малая модель, качество на уровне GPT Image 2 при меньшей задержке. Совет OpenAI: сначала проверить Sunburst, потом попробовать Flare на тех же промптах. Новость о двойной цене 2.5 относительно GPT Image 2 [В, jopr.org] противоречит официальной странице цен (у `gpt-image-2` те же $8/$30): верить странице цен.

### 3.1 Расчёт цены OpenAI

В исходнике официального калькулятора на странице гайда [О] зашита формула числа выходных токенов. Я воспроизвёл её (`openai_image_tokens.py` рядом) и сверил с официальной таблицей gpt-image-2 ($0,053 / $0,041 / $0,041 для medium) и с примером OpenRouter (high 1536x864 = $0,13; формула даёт $0,1294). Цена выхода = токены × $30 / 1 000 000.

```
base = { gpt-image-2:   low 16, medium 48, high 96,
         gpt-image-2.5: low 16, medium 24, high 48, xhigh 64, max 96 }
i = max(w,h); a = min(w,h); o = base[model][quality]
s = o / (i/a); u = round(s)   (ровно .5 округляется к чётному: l + l%2)
d = (w>=h ? o : u) * (w>=h ? u : o)
tokens = ceil( d * (2_000_000 + w*h) / 4_000_000 )
```

Цена выхода одной картинки, USD, без входных токенов [О, по формуле]:

| Размер | low | medium | high | xhigh | max |
|---|---|---|---|---|---|
| 2.5: 1024x1024 | 0,0059 (196 ток.) | 0,0132 (439) | 0,0527 (1756) | 0,0937 (3122) | 0,2107 (7024) |
| 2.5: 1536x1024 или 1024x1536 | 0,0047 | 0,0103 | 0,0412 | 0,0738 | 0,1646 |
| 2.5: 2048x2048 (2K) | 0,0119 | 0,0268 | 0,1070 | 0,1903 | 0,4282 |
| 2.5: 3840x2160 (4K) | 0,0111 | 0,0260 | 0,1001 | 0,1779 | 0,4003 |
| gpt-image-2: 1024x1024 | 0,0059 | 0,0527 | 0,2107 | - | - |
| gpt-image-2: 2048x2048 | 0,0119 | 0,1070 | 0,4282 | - | - |

Выводы:
- Уровень medium у 2.5 (439 токенов) в 4 раза дешевле medium у gpt-image-2 (1756). high у 2.5 стоит как medium у gpt-image-2, max у 2.5 как high у gpt-image-2.
- Широкий формат иногда дешевле квадрата.
- `quality=auto` и `size=auto` дают непредсказуемую цену: в интерфейсе запретить или показывать худший случай.
- Входные токены считаются отдельно: текст $5/М (короткий промпт около $0,0005) и токены входных картинок $8/М при edit. Сколько токенов на одну входную картинку у gpt-image-2/2.5, OpenAI официально не публикует; точное число приходит в `usage.input_tokens_details.image_tokens`. Формулу вывели на форуме OpenAI (тема «OpenAI must document the input image pricing of gpt-image-2», апрель 2026) [В]: токен за патч 32 px, не больше 1536 патчей, картинка с длинной стороной меньше 1024 сначала увеличивается до 1024 (не больше чем вдвое), холст длиннее 3:1 считается как 3:1. Запуск на тестовом боте 30.10.2026 с ней совпал: gpt-image-2.5-flare, фото 1280x853 дало 1080 токенов. framy считает по ней (`openai_input_patches` в overlay).
- Кэш входа ($2/М) работает только через Responses API, не через `/images/edits`.
- Batch (`/v1/batch`) вдвое дешевле, для интерактива не подходит.

## 4. Модели Gemini (Nano Banana)

### 4.1 Свойства и цены (AI Studio, платный tier, standard) [О]

| Модель | Алиас | Разрешения | Рефы | Thinking | Тарифы | Цена за картинку |
|---|---|---|---|---|---|---|
| `gemini-nano-banana-2.1` | Nano Banana 2.1 | 1K, 2K, 4K (512px нет) | до 14 (10 объектов + 4 персонажа) | minimal / medium (по умолч.) / high | вход $1,50/М, текст и мысли $7,50/М, картинка $30/М | 1K $0,0336, 2K $0,0504, 4K $0,113 |
| `gemini-3.1-flash-image` | Nano Banana 2 | 512px, 1K, 2K, 4K | до 14 (10 + 4) | minimal (по умолч.) / high | вход $0,50/М, текст $3/М, картинка $60/М | 512 $0,045, 1K $0,067, 2K $0,101, 4K $0,151 |
| `gemini-3.1-flash-lite-image` | Nano Banana 2 Lite | только 1K | до 14 объектов | minimal / high | вход $0,25/М, текст $1,50/М, картинка $30/М | 1K $0,0336 |
| `gemini-3-pro-image` | Nano Banana Pro | 1K, 2K, 4K | до 14 (6 объектов + 5 персонажей + 3 стилевых) | не настраивается | вход $2/М, текст $12/М, картинка $120/М | 1K и 2K $0,134, 4K $0,24 |
| `gemini-2.5-flash-image` | Nano Banana (легаси) | 1K (1290 ток.) | лучше до 3 | нет | вход $0,30/М, картинка $30/М | $0,039 |

- Токены на картинку: 1K = 1120, 2K = 1680, 4K = 2520 (3.1 Flash); у Pro 1K и 2K = 1120, 4K = 2000; 3.1 Flash 512px = 747.
- Расхождение: для Nano Banana 2.1 гайд даёт 4K = 2520 токенов ($0,0756), страница цен пишет 3780 токенов и $0,113. Платить по странице цен: в UI $0,113.
- Вход картинки: у Pro задокументировано 560 токенов ($0,0011 за картинку); у остальных не нашёл [?].
- Batch Google вдвое дешевле (1K у 2.1 $0,0168), но только в `generateContent`, срок до 24 часов.
- Расхождение по `gemini-2.5-flash-image`: страница цен "выключат 2026-10-02", страница deprecations "2027-03-15". Новые проекты на неё не переводить.
- Превью-ID (`gemini-3.1-flash-image-preview`, `gemini-3-pro-image-preview`) по deprecations выключены 2026-06-25; на OpenRouter висят дубли: не включать.
- Lite на 1K стоит как 2.1 ($0,0336), выигрыш только в скорости; 2.1 качественнее.

### 4.2 Размеры в пикселях [О]

Nano Banana 2.1 и 3.1 Flash Image:

| aspect_ratio | 1K | 2K | 4K |
|---|---|---|---|
| 1:1 | 1024x1024 | 2048x2048 | 4096x4096 |
| 2:3 / 3:2 | 848x1264 / 1264x848 | 1696x2528 / 2528x1696 | 3392x5056 / 5056x3392 |
| 3:4 / 4:3 | 896x1200 / 1200x896 | 1792x2400 / 2400x1792 | 3584x4800 / 4800x3584 |
| 4:5 / 5:4 | 928x1152 / 1152x928 | 1856x2304 / 2304x1856 | 3712x4608 / 4608x3712 |
| 9:16 / 16:9 | 768x1376 / 1376x768 | 1536x2752 / 2752x1536 | 3072x5504 / 5504x3072 |
| 21:9 | 1584x672 | 3168x1344 | 6336x2688 |
| 1:4 / 4:1 | 512x2048 / 2048x512 | 1024x4096 / 4096x1024 | 2048x8192 / 8192x2048 |
| 1:8 / 8:1 | 384x3072 / 3072x384 | 768x6144 / 6144x768 | 1536x12288 / 12288x1536 |

Pro: те же 10 соотношений без 1:4, 4:1, 1:8, 8:1. Lite: только 1K, 10 соотношений (1:1, 2:3, 3:2, 3:4, 4:3, 4:5, 5:4, 9:16, 16:9, 21:9). 3.1 Flash на 512px: 512x512, 424x632 и т. д.

## 5. Модели через OpenRouter

Это всё, что `GET /api/v1/images/models` вернул 2026-10-26 (61 запись), включая дубли OpenAI и Google. Наборы соотношений сторон:
- A9 = 1:1, 4:3, 3:4, 3:2, 2:3, 16:9, 9:16, 21:9, auto. A8 = A9 без 21:9.
- G10 = 1:1, 2:3, 3:2, 3:4, 4:3, 4:5, 5:4, 9:16, 16:9, 21:9. G14 = G10 + 1:4, 4:1, 1:8, 8:1.
- O4 = 1:1, 3:2, 2:3, auto.
- R6 = 1:1, 4:3, 3:4, 16:9, 9:16, auto. RS12 = 1:1, 2:1, 1:2, 3:2, 2:3, 4:3, 3:4, 5:4, 4:5, 16:9, 9:16, auto.
- K7 = 1:1, 4:3, 3:2, 16:9, 4:5, 2:3, 9:16.
- Q13 = 1:1, 1:2, 1:4, 2:1, 2:3, 3:2, 3:4, 4:1, 4:3, 4:5, 5:4, 9:16, 16:9.
- S18 = 1:1, 1:2, 2:1, 2:3, 3:2, 3:4, 4:3, 4:5, 5:4, 9:16, 16:9, 9:19.5, 19.5:9, 9:20, 20:9, 9:21, 21:9, auto.
- F16 = 21:9, 2:1, 16:9, 3:2, 7:5, 4:3, 5:4, 1:1, 4:5, 3:4, 5:7, 2:3, 9:16, 1:2, 9:21, auto.
- X14 = 1:1, 3:4, 4:3, 9:16, 16:9, 2:3, 3:2, 9:19.5, 19.5:9, 9:20, 20:9, 1:2, 2:1, auto.

### 5.1 Параметры Images API по моделям [OR, живой API]

refs это диапазон `input_references` (0 = можно только текст, 1.. = референс обязателен); n это допустимое число картинок за запрос.

| ID (OpenRouter) | refs | n | resolution | aspect_ratio | другие параметры | passthrough (`provider.options.<slug>`) |
|---|---|---|---|---|---|---|
| `black-forest-labs/flux-3-image` | 0..10 | 1 | 768,1K,1.5K,2K,4K | F16 | - | safety_tolerance |
| `black-forest-labs/flux-3-image-klein` | 0..10 | 1 | 768,1K,1.5K,2K,4K | F16 | - | safety_tolerance |
| `black-forest-labs/flux.2-flex` | 0..8 | 1 | - | A9 | output_format=png,jpeg; seed | guidance, safety_tolerance, steps |
| `black-forest-labs/flux.2-klein-4b` | 0..4 | 1 | - | A9 | output_format=png,jpeg; seed | guidance, safety_tolerance, steps |
| `black-forest-labs/flux.2-max` | 0..8 | 1 | - | A9 | output_format=png,jpeg; seed | guidance, safety_tolerance, steps |
| `black-forest-labs/flux.2-pro` | 0..8 | 1 | - | A9 | output_format=png,jpeg; seed | guidance, safety_tolerance, steps |
| `bytedance-seed/seedream-4.5` | 0..14 | 1..10 | 1K,2K,4K | S18 | seed | - |
| `bytedance-seed/seedream-5-0-flash` | 0..14 | 1 | 1K,2K | S18 | seed | - |
| `bytedance-seed/seedream-5-0-lite` | 0..14 | 1..4 | 2K,4K | S18 | seed | - |
| `bytedance-seed/seedream-5-0-pro` | 0..14 | 1 | 1K,2K | S18 | seed | - |
| `krea/k3-edit` | 1..8 | 1 | 1K,2K | K7 | seed | preserve_references, skip_prompt_expansion, thinking_mode |
| `krea/krea-2-large` | 0..1 | 1 | 1K | K7 | seed | complexity, creativity, image_style_references, intensity, moodboards, movement, strength, styles |
| `krea/krea-2-medium` | 0..1 | 1 | 1K | K7 | seed | то же |
| `krea/krea-2-medium-turbo` | 0..1 | 1 | 1K | K7 | seed | то же |
| `qwen/qwen-image-2.1-pro` | 0..4 | 1..6 | 1K,2K | Q13 | seed | - |
| `qwen/qwen-image-2.1-turbo` | 0..4 | 1..6 | 1K,2K | Q13 | seed | - |
| `qwen/qwen-image-3` | 0..3 | 1..6 | 1K,2K | Q13 | seed | - |
| `qwen/qwen-image-3-pro` | 0..3 | 1..6 | 1K,2K | Q13 | seed | - |
| `recraft/recraft-v3`, `recraft-v4`, `recraft-v4-pro`, `recraft-v4.1`, `recraft-v4.1-pro`, `recraft-v4.1-utility`, `recraft-v4.1-utility-pro` | 0..1 | 1..6 | - | R6 | - | controls, style, text_layout |
| `recraft/recraft-v4-vector`, `recraft-v4-pro-vector`, `recraft-v4.1-vector`, `recraft-v4.1-pro-vector` | 0..1 | 1..6 | - | R6 | output_format=svg | controls, style, text_layout |
| `recraft/recraft-v4.1-flash` | 0 (только T2I) | 1..6 | - | R6 | - | controls |
| `recraft/recraft-v4-styles`, `-styles-pro` | 1..10 | 1..6 | - | RS12 | - | controls, random_seed, style_id, style_match |
| `recraft/recraft-v4-styles-vector`, `-styles-pro-vector` | 1..10 | 1..6 | - | RS12 | output_format=svg | controls, random_seed, style_id, style_match |
| `sourceful/riverflow-v2-fast` | 0..4 | 1 | 1K,2K (OpenRouter перечисляет и 4K, модель его не делает) | A9 | - | font_inputs |
| `sourceful/riverflow-v2-pro` | 0..10 | 1 | 1K,2K,4K | A9 | - | font_inputs |
| `sourceful/riverflow-v2.5-fast` | 0..4 | 1 | 1K,2K | A9 | background=auto,transparent,opaque; output_format=jpeg | font_inputs |
| `sourceful/riverflow-v2.5-pro` | 0..10 | 1 | 1K,2K,4K | A9 | background=auto,transparent,opaque; output_format=png,jpeg,webp | font_inputs |
| `x-ai/grok-imagine-image-2.0` | 0..3 | 1 | 1K,2K | X14 | quality=low,medium | - |
| `x-ai/grok-imagine-image-quality` | 0..3 | 1 | 1K,2K | X14 | - | - |
| `microsoft/mai-image-2.5`, `-2.5-pro` | 0..1 | 1 | - | A8 | - | - |
| `microsoft/mai-image-2.6`, `-2.6-flash` | 0..5 | 1 | - | A8 | - | web_grounding |
| `tencent/hy-image-v3.5-preview` | 0..20 | 1 | 1K,1.5K,2K,4K | Q13 | seed, size | - |
| `meta/muse-image` | в Images API не представлена (только chat/completions) | | | | | |
| `google/gemini-3-pro-image` | 0..14 | 1 | 1K,2K,4K | G10 | - | cachedContent |
| `google/gemini-3.1-flash-image` | 0..14 | 1 | 512,1K,2K,4K | G14 | - | cachedContent |
| `google/gemini-3.1-flash-lite-image` | 0..14 | 1 | 1K | G14 | - | cachedContent |
| `google/gemini-nano-banana-2.1` | 0..14 | 1 | 1K,2K,4K | G14 | - | cachedContent |
| `google/gemini-2.5-flash-image` | 0..3 | 1 | - | G10 | - | cachedContent |
| `google/gemini-3-pro-image-preview`, `gemini-3.1-flash-image-preview` | дубли превью, не использовать | | | | | |
| `openai/gpt-image-2.5-flare`, `gpt-image-2.5-sunburst` | 0..16 | 1..10 | - | A9 | quality=auto,low,medium,high,xhigh,max; background=auto,transparent,opaque; output_compression=0..100 | moderation |
| `openai/gpt-image-2` | 0..16 | 1..10 | - | A9 | quality=auto,low,medium,high; background=auto,opaque; output_compression | moderation |
| `openai/gpt-5.4-image-2`, `gpt-5-image`, `gpt-5-image-mini` | 0..16 | 1..10 | - | A9 / O4 | quality, background, output_compression | moderation |
| `openai/gpt-image-1`, `gpt-image-1-mini` | 0..16 | 1..10 | - | O4 | quality, background, output_compression | moderation (модели выключены или выключаются) |

Для OpenAI-моделей через OpenRouter в дескрипторе нет `resolution` и `size`; работает ли общий `size` (пиксели), не проверял [?]. Поэтому 2K/4K у OpenAI надёжнее брать через прямой API.

### 5.2 Цены OpenRouter за запрос, USD [OR, живой API, эндпоинты]

| Модель | Цена выхода | Плата за входные картинки | Примечание |
|---|---|---|---|
| `black-forest-labs/flux-3-image` | 768: 0,041; 1K: 0,048; 1,5K: 0,070; 2K: 0,100; 4K: 0,607 | не указана | 4K около 16 Мпикс; T2I и edit одной цены [О, BFL docs: тиры; цены в BFL docs скрыты, цены из OR] |
| `black-forest-labs/flux-3-image-klein` | 768: 0,008; 1K: 0,009; 1,5K: 0,011; 2K: 0,014; 4K: 0,036 | не указана | открытые веса, 5,6 млрд параметров |
| `black-forest-labs/flux.2-pro` | 1-й Мпикс 0,03, каждый следующий 0,015 (1 Мп 0,03; 2 Мп 0,045; 4 Мп 0,075) | 0,015 за Мпикс | до 4 Мп; округление Мпикс не нашёл [?] |
| `black-forest-labs/flux.2-max` | 1-й Мп 0,07, следующие 0,03 (1 Мп 0,07; 4 Мп 0,16) | 0,03 за Мпикс | |
| `black-forest-labs/flux.2-flex` | 0,06 за Мпикс (1 Мп 0,06; 4 Мп 0,24) | 0,06 за Мпикс | сильна в тексте и типографике; есть `steps`, `guidance` |
| `black-forest-labs/flux.2-klein-4b` | 1-й Мп 0,014, следующие 0,001 | нет | самая дешёвая в FLUX.2 |
| `bytedance-seed/seedream-4.5` | 0,04 при любом размере | 0 | n до 10 |
| `bytedance-seed/seedream-5-0-flash` | 0,018 (1K и 2K) | 0 | |
| `bytedance-seed/seedream-5-0-lite` | 0,035 (2K и 4K) | 0 | n до 4 |
| `bytedance-seed/seedream-5-0-pro` | 0,045 по умолчанию, 0,09 (вариант `high_resolution`) | 0,003 за ref | какой размер = `high_resolution`, не раскрыто [?] |
| `krea/krea-2-medium-turbo` | 0,015 | - | со стилевыми референсами 0,0175, с мудбордами 0,02 |
| `krea/krea-2-medium` | 0,03 | - | 0,035 / 0,04 |
| `krea/krea-2-large` | 0,06 | - | 0,065 / 0,07 |
| `krea/k3-edit` | 1K 0,03; 2K 0,06; +0,02 за запрос при high thinking | 1K 0,02 за ref; 2K 0,07 за ref | 8 референсов в 2K = 0,06 + 8×0,07 = 0,62; в 1K = 0,19 |
| `qwen/qwen-image-2.1-turbo` | 0,0184 (1K и 2K) | 0 | |
| `qwen/qwen-image-2.1-pro` | 0,046 (1K и 2K) | 0 | |
| `qwen/qwen-image-3` | 0,03 (1K и 2K) | 0,003 за ref | |
| `qwen/qwen-image-3-pro` | 1K 0,04; 2K 0,075 | 0,003 за ref | |
| `recraft/recraft-v4.1-flash` | 0,007 | не принимает | только T2I, около 1,5 с |
| `recraft/recraft-v4.1`, `-utility` | 0,035 | - | около 1K |
| `recraft/recraft-v4.1-pro`, `-utility-pro` | 0,21 | - | около 2K |
| `recraft/recraft-v4`, `recraft-v3` | 0,04 | - | |
| `recraft/recraft-v4-pro` | 0,25 | - | около 2K |
| `recraft/recraft-v4.1-vector`, `recraft-v4-vector` | 0,08 | - | SVG |
| `recraft/recraft-v4.1-pro-vector`, `recraft-v4-pro-vector` | 0,30 | - | SVG |
| `recraft/recraft-v4-styles` / `-pro` / `-vector` / `-pro-vector` | 0,035 / 0,10 / 0,05 / 0,12 за картинку | единоразово 0,005 за запрос | нужен минимум 1 референс стиля; запрос с 1 картинкой стоит 0,040 / 0,105 / 0,055 / 0,125 |
| `sourceful/riverflow-v2-fast` | 1K 0,02; 2K 0,04; 4K не поддерживается (по описанию) | шрифт 0,03 (макс. 2); `super_resolution_references` 0,20 за ref (макс. 4) | |
| `sourceful/riverflow-v2-pro` | 1K и 2K 0,15; 4K 0,33 | то же | |
| `sourceful/riverflow-v2.5-fast` | от 0,019 (1K), от 0,021 (2K) | - | динамическая цена: итог известен по завершении (зависит от `reasoning`, разрешения, сложности) |
| `sourceful/riverflow-v2.5-pro` | от 0,13 (1K), 0,15 (2K), 0,17 (4K) | - | динамическая цена |
| `x-ai/grok-imagine-image-2.0` | low 1K 0,04; low 2K 0,06; medium 1K 0,06; medium 2K 0,08 | 0,01 за ref | |
| `x-ai/grok-imagine-image-quality` | 1K 0,05; 2K 0,07 | 0,01 за ref | |
| `microsoft/mai-image-2.6-flash` | токены: выход $19/М, вход текста $1,75/М, вход картинки $2,5/М | | 1024x1024 около 0,0195 [В, Artificial Analysis] (около 1024 токенов на картинку; официально не публикуется [?]) |
| `microsoft/mai-image-2.6` | выход $38/М, вход текста $5/М, вход картинки $8/М | | около 0,0389 за 1024x1024 [В] |
| `microsoft/mai-image-2.5` / `-pro` | выход $47/М и $108/М | | около 0,048 и 0,108 [В] |
| `tencent/hy-image-v3.5-preview` | токены, $1,6/М | входные картинки не тарифицируются (новости [В]) | AA: $0,024 за 1024x1024 [В]; другая новость: $0,024 за 2K [В]; токены на 1K/2K/4K не нашёл [?] |
| `meta/muse-image` | около 0,01 (страница OR и AA) | | slug содержит `eval`, в Images API нет, только chat; модерируется |
| `google/*`, `openai/*` | как в разделах 3 и 4, без наценки | | у `gemini-3.1-flash-image` есть Vertex-эндпоинт по $66/М вместо $60/М; OR выбирает по цене |

Примечания:
- Верхнеуровневое `pricing.image_output` в `/api/v1/models` у "поштучных" моделей это пересчёт минимальной цены на условные 4175 токенов. Для расчёта используйте `/images/models/{id}/endpoints`.
- Самые дешёвые на OR: `recraft-v4.1-flash` ($0,007), `flux-3-image-klein` ($0,008-0,009), `muse-image` (~$0,01, eval), `krea-2-medium-turbo` ($0,015), `seedream-5-0-flash` ($0,018), `qwen-image-2.1-turbo` ($0,0184), `riverflow-v2.5-fast` ($0,019), `mai-image-2.6-flash` (~$0,0195).

## 6. Возможности

T2I = текст→картинка; Edit = редактирование по картинке; refs = максимум входных картинок; Mask = настоящая маска (inpainting); Alpha = прозрачный фон; Text = качество текста (только заявления вендоров, сам рендер текста я не тестировал).

| Модель | T2I | Edit | refs | Mask | Alpha | Text (по заявлению вендора) |
|---|---|---|---|---|---|---|
| `gpt-image-2.5-sunburst` | да | да | 16 | да (PNG alpha) | да | улучшен, но "может путать точное расположение" [О]; AA: №1 в T2I и в edit |
| `gpt-image-2.5-flare` | да | да | 16 | да | да | то же [О] |
| `gpt-image-2` | да | да | 16 | да | preview | [О] |
| `gemini-nano-banana-2.1` | да | да | 14 | нет (текстом) | не документировано [?] | "улучшен" [О] |
| `gemini-3.1-flash-image` | да | да | 14 | нет | [?] | "улучшенный i18n" [OR] |
| `gemini-3.1-flash-lite-image` | да | да, локальные правки | 14 | нет | [?] | - |
| `gemini-3-pro-image` | да | да | 14 (6/5/3) | нет | [?] | "лучший рендер текста, длинные и многоязычные" [OR] |
| `flux-3-image` | да | да | 10 | нет | нет | - |
| `flux-3-image-klein` | да | да | 10 | нет | нет | текст заявлен [OR] |
| `flux.2-*` | да | да | pro/max/flex 8, klein 4 | нет | нет | flex: "сложный текст и типографика" [OR] |
| `seedream-5-0-*`, `seedream-4.5` | да | да | 14 | нет | нет | 4.5: "мелкий текст" [OR] |
| `qwen-image-3`, `-3-pro` | да | да | 3 | нет | нет | детали и текст от 10 px [OR] |
| `qwen-image-2.1-*` | да | да | 4 | нет | нет | - |
| `hy-image-v3.5-preview` | да | да, многоходовое | 20 | нет | нет | китайский и английский текст [OR] |
| `grok-imagine-image-*` | да | да | 3 | нет | нет | "чистый многоязычный текст" [OR] |
| `mai-image-2.6`, `-flash` | да | да | 5 | нет | нет | - |
| `mai-image-2.5`, `-pro` | да | 1 ref | 1 | нет | нет | - |
| `krea-2-*` | да | 1 ref | 1 | нет | нет | - |
| `krea/k3-edit` | нет (ref обязателен) | да | 1..8 | нет | нет | - |
| `recraft-v4*`, `v3` | да | 1 ref | 1 | нет | нет (есть цвет фона `background_rgb_color`) | V4: "читаемый встроенный текст"; `text_layout` по документации Recraft только у V3, хотя OpenRouter пропускает его и для V4.x |
| `recraft-v4-styles*` | по стилю | 1..10 референсов стиля | 10 | нет | нет | - |
| `recraft-*-vector` | да | 1 ref | 1 | нет | SVG | - |
| `riverflow-v2-*` | да | да | fast 4 / pro 10 | нет | нет | pro: "идеальный текст", `font_inputs` [OR] |
| `riverflow-v2.5-*` | да | да | fast 4 / pro 10 | нет | да (`background`, `background_mode`) | `font_inputs`, `scoring_prompt` |
| `meta/muse-image` | да | да | не раскрыто | нет | нет | "точный рендер текста" [OR] |

Настоящая маска в API подтверждена только у OpenAI. У остальных редактирование идёт промптом по референсам; в Images API OpenRouter параметра маски нет.

Альбом из нескольких фото и одна инструкция (в Telegram альбом до 10 медиа): подходят модели с refs от 10: GPT Image (16), Gemini (14), Seedream (14), Hy 3.5 (20), FLUX.3 (10), Riverflow Pro (10). Меньше 10: FLUX.2 (8; klein 4), K3 Edit (8), MAI 2.6 (5), Qwen (3-4), Grok (3), Recraft и Krea 2 (1). У Gemini 14 это сумма: у 2.1/3.1 Flash внутри лимит 10 объектов + 4 персонажа, у Pro 6/5/3.

## 7. Настройки: общее и уникальное

### 7.1 Общее (кандидаты на единую панель)

| Настройка | Где есть | Замечание |
|---|---|---|
| prompt | все | OpenAI до 32 000 символов; для остальных лимит не нашёл [?] |
| aspect_ratio | все | наборы значений разные, брать из `supported_parameters`; у прямого OpenAI вместо него `size` |
| resolution / image_size: 512, 768, 1K, 1.5K, 2K, 4K | Gemini, FLUX.3, Seedream, Qwen, Riverflow, Grok, Hy, Krea | у OpenAI `size` WxH; у FLUX.2, Recraft, MAI, Muse выбора нет (FLUX.2 по Мпикс) |
| число референсов | все | от 0 до 20 |
| n | OpenAI 1..10, Seedream 4.5 1..10, Qwen 1..6, Recraft 1..6, Seedream Lite 1..4; остальные 1 | для n=1 варианты = параллельные запросы |
| seed | FLUX.2, Seedream, Krea, Qwen, Hy (в Recraft Styles `random_seed`) | нет у OpenAI и Gemini в Images API |
| output_format | OpenAI (png/jpeg/webp), FLUX.2 (png/jpeg), Riverflow, Recraft vector (svg) | у остальных не выбирается |
| quality | OpenAI (low/medium/high, у 2.5 ещё xhigh/max), Grok 2.0 (low/medium) | главный регулятор цены у OpenAI |
| background | OpenAI (transparent/opaque), Riverflow 2.5 | |

### 7.2 Уникальное

| Модель / семейство | Настройки (точные имена) |
|---|---|
| OpenAI | `mask`, `moderation=auto|low`, `output_compression`, `partial_images`/`stream`, произвольный `size` WxH, `input_fidelity` только для не-2.x, `action` и `previous_response_id` в Responses API |
| Gemini | `thinking_level`, `tools: google_search` + `search_types: image_search`, `response_format` с текстом и картинкой, `previous_interaction_id` |
| FLUX.2 | passthrough `safety_tolerance`; `steps` и `guidance` только у Flex (пример OR: `steps: 40`, `guidance: 3`), в схеме BFL у Max, Pro и Klein 4B их нет (сверка 28.10.2026) |
| FLUX.3 | passthrough `safety_tolerance` |
| Krea 2 | passthrough `styles`, `image_style_references`, `moodboards`, `creativity`, `intensity`, `complexity`, `movement`, `strength` |
| Krea K3 Edit | passthrough `preserve_references`, `skip_prompt_expansion`, `thinking_mode` (значения не нашёл [?]) |
| Recraft V3/V4/V4.1 | passthrough `style`, `controls`, `text_layout` (у Flash только `controls`); по документации Recraft `style`, `text_layout`, `negative_prompt`, `artistic_level` и `no_text` работают только у V3, а V4 и V4.1 берут из `controls` лишь цвета и фон (сверка 28.10.2026); по описанию ещё `strength`, `rgb_colors`, `background_rgb_color` как `image_config` (в Images API не отражены [?]) |
| Recraft V4 Styles | `style_id`, `style_match`, `controls`, `random_seed` |
| Riverflow V2 | `font_inputs` (до 2, $0,03/шрифт), `super_resolution_references` ($0,20/ref, до 4) |
| Riverflow V2.5 | `reasoning` effort `low|medium|high` (Pro ещё `xhigh`), `font_inputs`, `scoring_prompt`, `scoring_rubric`, `background_mode` (`original|transparent|solid`), `background_hex_color` |
| MAI-Image 2.6 | passthrough `web_grounding` |

Negative prompt: не поддерживается ни одной из этих моделей через три провайдера. `style` у OpenAI для GPT image моделей запрещён [О].

## 8. Скорость и ограничения контента

Скорость (только найденное; остальное [?]):
- `gpt-image-2` high 16:9 1536x864: 94 секунды (пример в доке OpenRouter) [OR]. OpenAI: сложные промпты "до 2 минут" [О]. Flare заявлен как быстрый, числа у OpenAI нет; новость говорит о задержке на 50% ниже, чем у GPT-Image-2 [В].
- `gemini-3.1-flash-lite-image`: около 4 секунд (описание OR, "в 2,7 раза быстрее 3.1 Flash") [OR]; на странице Google цель "менее 2 секунд" [О]. Ориентироваться на 2-4 с.
- Recraft: V4.1 Flash около 1,5 с; V4.1 и Utility около 6 с; Pro и Utility Pro около 10 с; Vector около 10 с; Pro Vector около 15 с [OR].
- `krea/k3-edit`: большинство правок быстрее 10 с [OR].
- Остальное не публикуется [?].

Контент:
- OpenAI: серверная модерация, `moderation=auto|low`; ошибка `moderation_blocked` с `moderation_stage` (input/output) и категорией [О]. OR помечает OpenAI-модели и Muse как `is_moderated: true`, остальные `false` [OR].
- Google: Prohibited Use Policy; SynthID на всех картинках; граундинг по картинкам не использует фото реальных людей [О].
- FLUX: строгость через passthrough `safety_tolerance`; допустимые значения не нашёл [?].
- Правила других вендоров не изучал [?].

## 9. Бесплатный тариф: кандидаты (5 картинок в день, платит framy)

Elo: Artificial Analysis (AA-Image-T2I v2.1 и AA-Image-Editing v2.1), цена AA это 1000 картинок 1024x1024 по умолчанию [В]. Elo у OpenAI дан для режима `max`; на `medium` независимых измерений нет. Сопоставление имён AA и OpenRouter сделано по названиям и ценам; для Qwen 2.1 и Seedream 5.0 Pro цены AA не совпали с OR (AA $0,0353 против OR $0,046; AA $0,09 против OR $0,045 базовых), поэтому их Elo ненадёжны.

| Кандидат | Цена 1024x1024 | 5 в день × 30 дней | Elo T2I / Edit (AA) | Рефы | Замечания |
|---|---|---|---|---|---|
| `gpt-image-2.5-flare`, quality=medium | 0,0132 + вход | около 1,98 $ | 1207 / 1182 (при max) | 16 | прозрачность, маска, текст; качество на medium нужно проверить |
| `gpt-image-2.5-flare`, quality=low | 0,0059 + вход | около 0,88 $ | нет данных | 16 | самый дешёвый путь, качество не проверено |
| `flux-3-image-klein` (1K) | 0,009 | около 1,35 $ | 1043 / нет | 10 | открытые веса; слабее по Elo |
| `mai-image-2.6-flash` | около 0,0195 | около 2,92 $ | 1119 / 1144 | 5 | сильный edit за свою цену; только OR; Azure preview |
| `recraft-v4.1-flash` | 0,007 | около 1,05 $ | нет данных | 0 | только T2I, 1,5 с |
| `seedream-5-0-flash` | 0,018 | около 2,70 $ | нет данных | 14 | 1K и 2K по одной цене |
| `qwen-image-2.1-turbo` | 0,0184 | около 2,76 $ | нет сопоставления | 4 | n до 6 |
| `gemini-nano-banana-2.1` (1K) | 0,0336 | около 5,04 $ | 1176 / 1151 | 14 | лучшая цена/качество, но в 2,5-5 раз дороже верхних строк |
| `meta/muse-image` | около 0,01 | около 1,50 $ | 1127 / 1129 | ? | в Images API нет, slug `eval`: рано |

Месячная цена это максимум при полном использовании лимита.

## 10. Выводы для framy

### 10.1 Бесплатный тариф

Рекомендую `gpt-image-2.5-flare` на ключе framy с `quality=medium` и размером 1024x1024 / 1536x1024 / 1024x1536 (около $0,010-0,013 за картинку плюс копейки за входные токены). Причины: самая низкая цена среди моделей с сильным редактированием, 16 референсов (хватает на любой альбом), прозрачный фон, маска, нормальный текст; в AA серия 2.5 в режиме max на первых двух местах. Риск: Flare на medium никто независимо не мерил. До запуска нужен свой A/B на 20-30 типичных запросах: Flare medium против Nano Banana 2.1 1K и FLUX.3 Klein. На общем ключе передавать `safety_identifier` (хэш пользователя) и учесть лимит 20 IPM на tier Build: для бесплатного тарифа нужен tier Launch (150 IPM) или выше.

Запасной вариант (отказ или модерация OpenAI): `black-forest-labs/flux-3-image-klein` через OpenRouter ($0,009, 10 референсов) или `microsoft/mai-image-2.6-flash` (около $0,0195, лучше редактирует). Если нужно качество выше при приемлемом бюджете: `gemini-nano-banana-2.1` 1K ($0,0336) на меньшее число в день (например 3).

Бесплатного Gemini для картинок нет: free tier у картиночных моделей "Not available".

### 10.2 Топовые модели (платные, ключ пользователя)

| Роль | Модель | Почему |
|---|---|---|
| Максимальное качество | `gpt-image-2.5-sunburst` | №1 по Elo в генерации (1222) и редактировании (1199); quality до `max` (до $0,21 за 1024x1024, $0,43 за 2K) |
| Лучшая цена/качество | `gemini-nano-banana-2.1` | Elo 1176/1151 за $0,0336-0,113; 4K; 14 референсов; поиск-граундинг; thinking |
| Премиум-тексты и макеты | `gemini-3-pro-image` | 4K за $0,24, 3 стилевых референса, 5 персонажей; по Elo (1118/1108) ниже 2.1 |
| Баланс | `microsoft/mai-image-2.6` | Elo 1166/1151 за около $0,039; 5 референсов |
| Фотореализм в большом разрешении | `black-forest-labs/flux-3-image` | Elo 1117/1122; 2K за $0,10; 4K $0,607 (дорого) |
| Много референсов | `tencent/hy-image-v3.5-preview` (20), `bytedance-seed/seedream-5-0-pro` (14) | |
| Векторы | `recraft/recraft-v4.1-vector` ($0,08), `recraft-v4.1-pro-vector` ($0,30) | единственные SVG |
| Стили по образцу | `recraft/recraft-v4-styles*` | стиль по 1-10 референсам |
| Контроль текста и шрифтов | `sourceful/riverflow-v2.5-pro` | `font_inputs`, `scoring_prompt`, `reasoning`, прозрачность; цена динамическая |

Не включать: `openai/gpt-image-1*`, `gpt-image-1.5`, `chatgpt-image-latest` (закрываются), `google/gemini-2.5-flash-image` (легаси), `*-preview` дубли Gemini, `openai/gpt-5*-image*` (чат-обёртки с токенами основной модели, дороже и непрозрачнее), `meta/muse-image` (eval), `openrouter/auto*`.

### 10.3 Единый интерфейс

1. Каталог моделей хранить в JSON-дескрипторах формата OpenRouter `supported_parameters` (`enum`, `range`, `boolean`) плюс тип биллинга. OpenRouter отдаёт такие данные для всех своих моделей; для прямых OpenAI и Gemini дескрипторы писать вручную по разделам 2-4.
2. Общая панель: промпт, референсы (0..N с лимитом модели), соотношение сторон, разрешение (если у модели есть выбор), число вариантов (n или параллельные запросы), формат вывода, seed (если поддерживается). Чего нет в дескрипторе модели, скрывать, а не показывать выключенным.
3. Группы уникальных настроек, появляющиеся только у своих моделей: OpenAI (quality до max, background, маска, moderation), Gemini (thinking_level, поиск), Riverflow (reasoning, шрифты, scoring), Recraft (стиль, цвета, SVG), Krea (стили, мудборды), FLUX.2 Flex (steps, guidance).
4. Расчёт цены до запуска, четыре типа биллинга:
   - таблица по тирам (FLUX.3, Seedream, Krea, Qwen, Recraft, Riverflow v2, Grok): цена = таблица[тир, quality] × n + плата за входные картинки (K3 Edit 0,02/0,07 за ref, Grok 0,01, Seedream Pro и Qwen 3 по 0,003);
   - за мегапиксель: FLUX.2;
   - токены по формуле: OpenAI (раздел 3.1) и Gemini (токены по разрешению × тариф): показывать как "около"; точная цена в `usage`;
   - динамическая: Riverflow V2.5, показывать "от X".
   Для OpenAI и Gemini входные картинки добавлять отдельной строкой с запасом; после запроса показывать фактическую цену из `usage` (у OpenRouter `usage.cost` в USD).
5. Не давать `quality=auto` и `size=auto` у OpenAI либо показывать худший случай.
6. Маршрут: OpenAI и Gemini напрямую (маска, 2K/4K у OpenAI, `thinking_level`, поиск), остальное через OpenRouter. Через OpenRouter у OpenAI нет `resolution`, у Gemini нет `thinking_level`.
7. Покрытие по ключам: ключ OpenRouter даёт почти весь каталог (OpenAI, Gemini и все сторонние), ключ OpenAI только 3 актуальные модели, ключ Gemini 4-5 моделей.
8. Понятные сообщения для ошибок: 403 про Verified Organization (OpenAI), нет биллинга (Gemini), 402 нет кредитов (OpenRouter), `moderation_blocked`, 413 слишком большой запрос (OpenRouter, Sourceful 4,5 МБ), 429.
9. Telegram-альбом до 10 фото: модели с лимитом refs меньше 10 обрезают альбом; предупреждать и считать цену за реально принятые референсы.

## 11. Что не удалось проверить

- Токены на одну входную картинку у gpt-image-2 и 2.5 при edit: формула с форума OpenAI совпала с одним запуском (раздел 3), официального описания нет.
- Требование Verified Organization для gpt-image-2.5-*.
- Округление мегапикселей у FLUX.2 и как на OpenRouter задаётся размер FLUX.2 (параметра `resolution` в дескрипторе нет).
- Токены на картинку у MAI-Image и Hy Image 3.5; цены Hy на 1K/2K/4K (источники расходятся).
- Допустимые значения passthrough: Recraft `style` и `controls`, Krea `thinking_mode`, BFL `safety_tolerance`.
- Имена полей `generateContent` для картинок.
- Есть ли у Gemini image `seed` и прозрачный фон.
- Дата выключения `gemini-2.5-flash-image` (две страницы Google называют разные даты).
- Токены 4K у Nano Banana 2.1 (гайд 2520, страница цен 3780).
- Скорость большинства моделей; правила контента большинства вендоров.
- Лимиты запросов по tier'ам у Gemini и OpenRouter для картиночных моделей.
- Elo AA сопоставлены с моделями OpenRouter по названиям; для "Krea 3 (Ultra/Fast)" соответствие `krea/k3-edit` не подтверждено.

## 12. Источники

Официальные [О]:
- OpenAI, гайд генерации: https://developers.openai.com/api/docs/guides/image-generation
- OpenAI, промптинг и сравнение Flare/Sunburst: https://developers.openai.com/api/docs/guides/image-prompting
- OpenAI, цены: https://developers.openai.com/api/docs/pricing
- OpenAI, модели: https://developers.openai.com/api/docs/models/gpt-image-2.5-sunburst , https://developers.openai.com/api/docs/models/gpt-image-2.5-flare , https://developers.openai.com/api/docs/models/gpt-image-2 , https://developers.openai.com/api/docs/models/gpt-image-1-mini , https://developers.openai.com/api/docs/models/gpt-image-1.5
- OpenAI, выключения: https://developers.openai.com/api/docs/deprecations
- OpenAI, параметры: https://developers.openai.com/api/reference/python/resources/images/methods/edit , https://developers.openai.com/api/reference/python/resources/images/methods/generate
- OpenAI, исходник калькулятора токенов: https://developers.openai.com/_astro/GptImageTokenCalculator.react.BaFlL4Zr.js
- Google, генерация картинок: https://ai.google.dev/gemini-api/docs/image-generation
- Google, цены: https://ai.google.dev/gemini-api/docs/pricing
- Google, выключения: https://ai.google.dev/gemini-api/docs/deprecations
- Google, модели: https://ai.google.dev/gemini-api/docs/models/gemini-nano-banana-2.1 (и .../gemini-3.1-flash-lite-image, .../gemini-3.1-flash-image, .../gemini-3-pro-image, .../gemini-2.5-flash-image)
- Google, Interactions API: https://ai.google.dev/gemini-api/docs/interactions-overview
- Google, лимиты: https://ai.google.dev/gemini-api/docs/rate-limits
- OpenRouter, Images API: https://openrouter.ai/docs/guides/overview/multimodal/image-generation , https://openrouter.ai/api/v1/images/models , https://openrouter.ai/api/v1/images/models/{id}/endpoints , https://openrouter.ai/api/v1/models?output_modalities=image
- OpenRouter, комиссии: https://openrouter.ai/pricing
- OpenRouter, страницы моделей (пример): https://openrouter.ai/black-forest-labs/flux-3-image

Вторичные [В]:
- Artificial Analysis: https://artificialanalysis.ai/image/leaderboard/text-to-image , https://artificialanalysis.ai/image/leaderboard/editing
- Запуск ChatGPT Images 2.5: https://jopr.org/news/detail/openai-launches-chatgpt-images-25
- Hy Image 3.5: https://latenode.com/news/tencent-launches-hy-image-3-5-preview-at-0-15-per-2k-output , https://runtimewire.com/article/tencent-hy-image-3-5-preview-multiturn-editing-api
- MAI-Image-2.6: https://runtimewire.com/article/microsoft-mai-image-2-6-foundry-four-cents-image
- Форум OpenAI о верификации организации: https://community.openai.com/t/gpt-images-1-403-what-are-the-conditions-for-requesting-this-model-gpt-image-1/1245846/2
- BFL docs (тиры разрешений FLUX.3, цены скрыты): https://docs.bfl.ai/quick_start/pricing
