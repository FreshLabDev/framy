# Сборка и инфраструктура framy: Cloudflare, файлы, видео, модерация, очередь, вход, ключи

Исследование 26.10.2026. Пометки. [док] - прочитано в официальной документации (URL рядом). [тест] - проверено мной: macOS arm64, Go 1.27.3, ffmpeg 9.0.1, живые запросы к oauth.telegram.org и web.telegram.org. [?] - не нашёл подтверждения, это вывод или память. Тестовые программы (imgtest, oidctest, vidtest) были во временном каталоге сессии и не сохранены.

## 1. Cloudflare Free + Tunnel

### 1.1 Лимиты и таймауты

| Что | Значение | Источник |
|---|---|---|
| Размер тела запроса (загрузка) | Free и Pro: 100 МБ, Business 200 МБ. Больше - 413 | [док] developers.cloudflare.com/support/troubleshooting/http-status-codes/4xx-client-error/error-413/ |
| Proxy Read Timeout | 125 с (ошибка 524), менять может только Enterprise. Цифра 100 с устарела | [док] developers.cloudflare.com/fundamentals/reference/connection-limits/ |
| Proxy Write Timeout | 30 с, не настраивается | там же |
| Proxy Idle Timeout | 900 с (520) | там же |
| Keep-Alive клиента | 400 с | там же |
| URL / заголовки | 16 КБ / 128 КБ | там же |
| Отдельные цифры для Tunnel | В документации нет. Лимиты Cloudflare те же | - |
| Дефолты cloudflared к origin | connectTimeout 30 с, tlsTimeout 10 с, keepAliveTimeout 90 с, keepAliveConnections 100, tcpKeepAlive 30 с | [док] developers.cloudflare.com/cloudflare-one/connections/connect-networks/configure-tunnels/cloudflared-parameters/origin-parameters/ |

524 приходит, если origin не прислал ответ за 125 с. Для долгих операций Cloudflare рекомендует поллинг статуса (developers.cloudflare.com/support/troubleshooting/http-status-codes/cloudflare-5xx-errors/error-524/). Генерация видео занимает минуты, поэтому HTTP-запрос не должен ждать результата: POST создаёт задачу и сразу отвечает, прогресс идёт по SSE или поллингу.

Загрузка. 100 МБ достаточно для фото. Для видео-референсов больше 100 МБ режьте на чанки или грузите напрямую в R2 (п. 1.5). DNS-only, который Cloudflare предлагает как обход, с Tunnel невозможен.

### 1.2 SSE и WebSocket через Tunnel

SSE работает, но с условием.
- Документация Tunnel: трафик буферизуется, если origin не отдаёт `Content-Type: text/event-stream` (developers.cloudflare.com/cloudflare-one/connections/connect-networks/troubleshoot-tunnels/common-errors/).
- Исходники cloudflared (`connection/connection.go`, master): flush на каждую запись, если нет Content-Length, или Transfer-Encoding chunked, или Content-Type начинается с `text/event-stream`, `application/grpc`, `application/x-ndjson`. Это коммит TUN-9255 от апреля 2025 [док по коду]: github.com/cloudflare/cloudflared/blob/master/connection/connection.go.
- Cloudflare не сжимает `text/event-stream` (нет в списке сжимаемых типов): developers.cloudflare.com/speed/optimization/content/compression/.

Что делать в Go:
- Заголовки: `Content-Type: text/event-stream`, `Cache-Control: no-cache, no-transform`. `X-Accel-Buffering` нужен только за nginx, у вас его нет.
- После каждого события вызывайте `http.NewResponseController(w).Flush()`.
- Заголовки отправьте сразу, не ждите первого события, иначе 125 с отсчитываются до первого ответа.
- Шлите комментарий `: ping\n\n` каждые 15-20 с. Это держит соединение живым и быстро ловит разрыв.
- `http.Server.WriteTimeout` убивает долгие потоки. Либо 0 для SSE-маршрута, либо `ResponseController.SetWriteDeadline` перед каждой записью.
- `EventSource` в браузере не умеет заголовок Authorization. Для Mini App с bearer-токеном нужен fetch-стриминг или одноразовый ticket в query. Это свойство веб-платформы [?], не из документации Cloudflare.
- При перезапуске cloudflared или контейнера поток рвётся. Клиент должен переподключаться с `Last-Event-ID`.

WebSocket: поддерживается на всех планах без дополнительной настройки (developers.cloudflare.com/network/websockets/) [док]. Таймаут простоя Cloudflare не публикует. Docs предупреждают, что при выкатке нового кода Cloudflare перезапускает серверы и рвёт соединения, и советуют ping/pong. Дайте ping каждые 20-30 с. WAF смотрит только на первоначальный Upgrade. Заголовки специально добавлять не надо. Для framy WebSocket не нужен: прогресс односторонний, хватит SSE и фолбэка на поллинг.

### 1.3 Кэш: что реально происходит с Cache-Control

[док] developers.cloudflare.com/cache/concepts/default-cache-behavior/ и /cache/how-to/edge-browser-cache-ttl/:
- Кэшируется по расширению: js, css, jpg, png, webp, avif, mp4, webm, svg, шрифты и т. д. HTML и JSON по умолчанию не кэшируются. robots.txt кэшируется.
- Не кэшируется при `Cache-Control: private | no-store | no-cache | max-age=0`, при `Set-Cookie`, при методе не GET. Кэшируется при `public` с `max-age>0`.
- Origin Cache Control включён по умолчанию на Free, то есть заголовки origin уважаются.
- «4 часа» - это Browser Cache TTL. Он перезаписывает заголовок origin только если значение origin меньше TTL (по умолчанию 4 ч). `max-age=31536000, immutable` проходит как есть, `immutable` Cloudflare пропускает дальше.
- Лечение: зона, Caching - Configuration - Browser Cache TTL = «Respect Existing Headers». Тогда без Cache Rule Cloudflare ничего не вставляет и не переписывает. Доступность пункта на Free явно не указана [?], проверьте в дашборде. Запасной вариант: Cache Rule с Browser TTL «Respect origin».
- Cache Rules на Free: 10 правил. Режимы Browser TTL: Bypass / Respect origin / Override. Edge TTL: «заголовок или bypass», «заголовок или дефолт Cloudflare», «игнорировать заголовок». developers.cloudflare.com/cache/how-to/cache-rules/ и /settings/.
- Максимальный размер файла в кэше: 512 МБ на Free. Range-запросы обслуживаются и из полного, и из частичного кэша.
- Влияет ли Browser Cache TTL на ответы `no-store` и `private`, документация не говорит [?]. Проверьте `curl -I` после выкладки.

Рекомендованная матрица:

| Путь | Заголовок от Go | Cache Rule |
|---|---|---|
| `/api/*`, `/auth/*`, `/events`, `/hooks/*` | `Cache-Control: no-store` | Bypass cache (ремень к подтяжкам, JSON и так не кэшируется) |
| `/assets/<hash>.js` и `.css` | `public, max-age=31536000, immutable` | Browser TTL: Respect origin |
| HTML-оболочка | `no-cache` + ETag | не кэшируется по умолчанию |
| Публичные картинки результатов `/s/<token>/…jpg` | `public, max-age=31536000, immutable` | не нужно, jpg кэшируется по расширению |
| Приватные результаты | `private, no-store`, отдача через presigned URL | - |

Главная ловушка. Cloudflare кэширует по URL, не глядя на куки. Ответ с `public` на авторизованный контент достанет из кэша любой, у кого есть URL. `public` ставьте только на публичные результаты с неугадываемым токеном.

### 1.4 Большие результаты: Tunnel или R2

Берите R2. Причина не техническая, а условия использования. Страница Cloudflare про доставку видео прямо говорит, что на Free, Pro и Business трафик публичных hostname через Tunnel подчиняется ограничению на видео и «непропорциональный объём крупных файлов»: «Cloudflare Tunnel public hostname routes proxy traffic through Cloudflare.» Исключены только private network routes. developers.cloudflare.com/fundamentals/reference/policies-compliances/delivering-videos-with-cloudflare/. Порога в процентах нет, санкции - редирект или иные меры, с уведомлением на почту.

Видео и крупные файлы можно отдавать через CDN, если они лежат в сервисе Cloudflare (Stream, Images, R2). Так сказано в блоге про новые ToS, 2023: blog.cloudflare.com/updated-tos/. Это даёт и бонус: домашний канал ws04 не раздаёт 100 МБ каждому зрителю, а seek по Range обслуживает R2.

Картинки результатов в 1-5 МБ через Tunnel формально тоже «крупные файлы», но риск ниже. Для единообразия кладите в R2 и их.

### 1.5 R2: цены и presigned

| Что | Цена | Источник |
|---|---|---|
| Хранение Standard | $0.015 / ГБ-мес, бесплатно 10 ГБ-мес | [док] developers.cloudflare.com/r2/pricing/ |
| Class A (PUT, LIST, multipart) | $4.50 / млн, бесплатно 1 млн/мес | там же |
| Class B (GET, HEAD) | $0.36 / млн, бесплатно 10 млн/мес | там же |
| Egress в интернет | бесплатно | там же |
| Infrequent Access | $0.01/ГБ, A $9, B $0.90 за млн, +$0.01/ГБ выдачи, минимум 30 дней | там же |

Пример: 100 ГБ хранения - (100-10) × 0.015 ≈ $1.35 в месяц, 1 ТБ ≈ $14.85. Запрашивает ли Cloudflare карту при включении R2 даже для free tier, официально не нашёл [?]. В сторонних руководствах пишут, что просит.

Лимиты (developers.cloudflare.com/r2/platform/limits/): один PUT до 5 ГиБ, не больше 1 записи в секунду в один ключ (иначе 429), ключ до 1024 байт. `r2.dev` имеет rate limit и только для разработки (developers.cloudflare.com/r2/buckets/public-buckets/). Lifecycle: удаление по префиксу через N дней, удаление происходит в течение примерно 24 ч, брошенные multipart-загрузки по умолчанию чистятся через 7 дней (developers.cloudflare.com/r2/buckets/object-lifecycles/).

Presigned URL (developers.cloudflare.com/r2/api/s3/presigned-urls/) [док]:
- Операции GET, HEAD, PUT, DELETE. POST с формой не поддерживается. Срок от 1 с до 7 суток.
- Работают только на домене `<ACCOUNT_ID>.r2.cloudflarestorage.com`, не на кастомном. На этом домене нет Cloudflare-кэша, но и egress бесплатный.
- Для браузерного PUT нужен CORS на бакете (пример с `AllowedMethods: PUT`, `AllowedHeaders: Content-Type`, `ExposeHeaders: ETag`: developers.cloudflare.com/r2/buckets/cors/). Content-Type лучше включить в подпись.
- Следствие запрета POST: нельзя использовать условие `content-length-range`, как в S3 POST policy. Это мой вывод, не цитата из документации. Размер проверяйте после загрузки: HEAD по объекту, лишнее удалять, плюс lifecycle на префикс `incoming/` с TTL 1 сутки.
- Go: aws-sdk-go-v2, `BaseEndpoint = https://<ACCOUNT_ID>.r2.cloudflarestorage.com`, регион `auto`, `s3.NewPresignClient().PresignPutObject` (developers.cloudflare.com/r2/examples/aws/aws-sdk-go/).

Кастомный домен для публичных результатов должен быть в той же учётной записи Cloudflare. Universal SSL покрывает только корень и первый уровень поддоменов: `media.framy.ws04.cc` сертификата не получит без платного Advanced Certificate Manager (developers.cloudflare.com/ssl/edge-certificates/universal-ssl/limitations/). Берите `framy-media.ws04.cc`.

## 2. Форматы картинок в Go

### 2.1 Лимиты

| Кто | Вход | Выход / замечания |
|---|---|---|
| Telegram sendPhoto | multipart: фото до 10 МБ, сумма ширины и высоты ≤ 10000, отношение сторон ≤ 20. По URL: 5 МБ | [док] core.telegram.org/bots/api |
| Telegram прочие файлы | multipart 50 МБ, по URL 20 МБ. Локальный сервер: загрузка до 2000 МБ, скачивание без лимита, загрузка по `file://` | то же |
| getFile на обычном сервере | 20 МБ | core.telegram.org/bots/faq |
| OpenAI edits | PNG, WebP, JPG, до 50 МБ каждый, до 16 картинок. Сторонний источник пишет 25 МБ для gpt-image-2, расхождение [?] | developers.openai.com/api/reference/cli/resources/images/methods/edit/index.md |
| OpenAI размеры выхода | Рекомендуются 1024², 1536×1024, 1024×1536. Произвольные: стороны кратны 16, до 3840, пропорции от 1:3 до 3:1, 0.65-8.3 Мпикс | developers.openai.com/api/docs/guides/image-generation |
| Gemini | PNG, JPEG, WEBP, HEIC, HEIF. Inline-данные: весь запрос ≤ 20 МБ. Больше - Files API | ai.google.dev/gemini-api/docs/image-understanding |
| OpenRouter Image API | `input_references[]` с `image_url`: https-ссылка или data URL. Форматы и размеры в документации не указаны, зависят от провайдера | openrouter.ai/docs/features/multimodal/image-generation |

Telegram отдаёт `photo` как набор размеров, берите последний (самый крупный). Документ может быть любого формата. Альбомы приходят отдельными апдейтами с общим `media_group_id`: нужна задержка 1-2 с на сборку, иначе каждая картинка запустит свою генерацию. При локальном Bot API `file_path` - абсолютный путь в файловой системе контейнера telegram-bot-api. Этот том нужно смонтировать в framy по тому же пути, обратите внимание на права.

iPhone и HEIC. Safari при загрузке иногда конвертирует HEIC в JPEG сам, зависит от версии iOS, списка `accept` и источника файла. Есть открытые баги WebKit, в том числе обратная конвертация PNG и JPEG в HEIC при `accept="image/heic"`: bugs.webkit.org/show_bug.cgi?id=292350, bugs.webkit.org/show_bug.cgi?id=303803. Вывод: принимать HEIC на сервере надо всегда, определять формат по байтам, а не по MIME.

### 2.2 Чем декодировать

Измерения на 12-Мпикс (4032×3024) изображении, mac arm64:

| Вариант | HEIC | AVIF | WebP | Память | Замечания |
|---|---|---|---|---|---|
| ffmpeg 9.0.1 (подпроцесс) [тест] | 0.13 с | 0.11 с | поддерживается нативным декодером, у меня не проверялось | отдельный процесс | Автоповорот по EXIF - да. Метаданные в выходе нет. HEIC-сетка (iPhone режет кадр на тайлы 512×512) собирается сама |
| gen2brain/heic v0.7.2 + wazero [тест] | 0.75 с | gen2brain/avif v0.6.0: 1.2 с | x/image/webp: 0.09 с | пик по `Sys` 430 МБ HEIC, 660 МБ AVIF | MIT, без cgo, бинарь с тремя декодерами 9.8 МБ. Применяет ориентацию сам. Анимированный WebP x/image/webp отвергает («invalid format»), это нормально |
| libvips (govips, cgo) | не мерил | не мерил | не мерил | - | Быстрее всех и с ICC, но новая нативная зависимость в образе, и вопрос сборки libheif с HEVC [?] |
| ImageMagick | - | - | - | - | Не рекомендую: медленный, история CVE, нужен `policy.xml` (моё мнение) |

Качество: HEIC через Go и через ffmpeg дают PSNR 46.3 дБ друг относительно друга [тест], то есть практически идентичны.

Что проверил про ffmpeg:
- Сетка HEIC в ffmpeg 9.0.1 - это stream group. Обычный `-vf scale` падает с «Simple and complex filtering cannot be used together». Рабочий вариант - `-filter_complex "[0:g:0]scale=…[o]" -map "[o]"` [тест].
- HEIC через stdin (`pipe:0`) не открывается: «Not yet implemented in FFmpeg». Нужен временный файл [тест].
- EXIF Orientation=6: ffmpeg по умолчанию повернул 4032×3024 в 3024×4032 [тест]. EXIF-сегмент и мой маркер `TestPhone` в выходном JPEG отсутствуют [тест]. GPS живёт в том же EXIF, так что уходит вместе с ним. Я не тестировал JPEG с настоящим GPS IFD.
- Минимальную версию ffmpeg для HEIC-сеток я не определял, подтверждено только на 9.0.1. Проверьте `ffmpeg -version` в вашем образе и прогоните настоящие файлы с iPhone (Portrait с depth, Live Photo, HDR gain map): у меня был HEIC, сделанный `sips`, он проще.
- Защита от враждебного входа: после сниффинга по магическим байтам кладите файл во временный каталог под именем, которое задаёте вы (`in.heic`, `in.jpg`), запускайте с `-protocol_whitelist file`, без `-f` от пользователя. Подсунутый HLS-плейлист под именем `.jpg` ffmpeg не распознал и отверг [тест].

Рекомендация: один подход - ffmpeg-подпроцесс. Он уже в образе, в 5-6 раз быстрее чистого Go на HEIC, ест память в отдельном процессе (можно ограничить `ulimit`/cgroup и таймаутом), единообразно делает ресайз, поворот и зачистку метаданных. Чистый Go (gen2brain/heic) держите как запасной вариант на случай, если в образе окажется ffmpeg без HEIC.

Пайплайн нормализации:
1. `http.MaxBytesReader` и потоковая запись в файл (не `ParseMultipartForm` в память).
2. Сниффинг по первым 32 байтам: JPEG `FFD8FF`, PNG, GIF, WebP `RIFF…WEBP`, HEIC/AVIF - `ftyp` с брендами `heic heix mif1 msf1 avif avis` (`http.DetectContentType` HEIC и AVIF не знает).
3. `ffprobe -print_format json -show_streams`: отклонить, если больше ~64 Мпикс или ширина × высота слишком велика (защита от декомпрессионных бомб), понять наличие альфы по `pix_fmt`.
4. `ffmpeg … -frames:v 1 -map_metadata -1` с ресайзом: длинная сторона ≤ 2048, `flags=lanczos`, на выход JPEG q≈3 (≈q90) или PNG, если есть альфа. base64 раздувает на треть, поэтому при нескольких референсах 2048 px JPEG укладывается в 20 МБ Gemini inline без Files API.
5. Размер на отдачу: для `sendPhoto` проверять 10 МБ и правила сторон. Результат OpenAI в PNG 4K может не пройти, делайте JPEG q92 с длинной стороной ≤ 2560 для превью и отдавайте оригинал отдельным `sendDocument`.

Цветовые профили. iPhone снимает в Display P3. ffmpeg и Go-декодеры профиль не конвертируют, цвета слегка тускнеют. Для референсов генерации это косметика. Если важно, нужен libvips с lcms [?].

## 3. Видео для Telegram

Что документировано [док] core.telegram.org/bots/api:
- Клиенты поддерживают MPEG4, остальное может уйти документом. Лимит 50 МБ, на локальном сервере 2000 МБ.
- Параметры `sendVideo`: `duration`, `width`, `height`, `supports_streaming`, `cover` (file_id, URL или `attach://`), `start_timestamp`, `thumbnail` (JPEG до 200 КБ, сторона до 320 px; можно не передавать, если миниатюру строит сервер).
- Про кодеки H.264/AAC и faststart Telegram в документации ничего не пишет, и `core.telegram.org/api/files` тоже [?]. Ниже практика, а не требования: проверьте на реальных клиентах iOS, Android, Desktop, Web.
- `sendAnimation`: GIF или H.264 без звука, до 50 МБ. Хорош для беззвучных петель.

Решение: перекодировать или ремуксить.
- Если по `ffprobe` кодек `h264`, `pix_fmt=yuv420p`, звук `aac` или его нет, размеры чётные, нет display-matrix поворота, размер в лимите, делайте только ремукс с faststart.
- Иначе (HEVC 10 бит, VP9/AV1 в webm, 4:4:4, нечётные размеры, поворот) - перекодирование.

Команды (проверены на ffmpeg 9.0.1) [тест]:

```
# ремукс, moov переезжает в начало
ffmpeg -i in.mp4 -c copy -movflags +faststart -map_metadata -1 out.mp4

# перекодирование: HEVC 10 бит -> H.264 High yuv420p, AAC, faststart
ffmpeg -i in.mp4 -map 0:v:0 -map '0:a:0?' \
  -vf "scale=trunc(iw/2)*2:trunc(ih/2)*2,format=yuv420p" \
  -c:v libx264 -preset veryfast -crf 21 -profile:v high \
  -c:a aac -b:a 128k -movflags +faststart -map_metadata -1 out.mp4
```

- Порядок атомов: у исходного файла `ftyp, free, mdat, moov`, после ремукса `ftyp, moov, free, mdat` [тест]. Кавычки вокруг `0:a:0?` обязательны в zsh.
- `width`, `height`, `duration` берите из `ffprobe` после обработки, а не от провайдера. `duration` целое число секунд. Выставляйте `supports_streaming=true`.
- Если есть звук, не удаляйте его без нужды. Беззвучное видео в `sendVideo` играет нормально, дорожку-пустышку добавлять не нужно.

Кадры:

```
# первый кадр
ffmpeg -i in.mp4 -frames:v 1 -q:v 2 first.jpg
# последний кадр (без -frames:v!)
ffmpeg -sseof -1 -i in.mp4 -update 1 -q:v 2 last.jpg
# миниатюра <=320 px, JPEG
ffmpeg -i in.mp4 -frames:v 1 -vf "scale='min(320,iw)':'min(320,ih)':force_original_aspect_ratio=decrease" -q:v 5 thumb.jpg
```

Последний кадр через `-sseof -1 -update 1` совпал с кадром n=71 из 72, вырезанным через `select=eq(n\,71)`: PSNR inf, пиксель в пиксель [тест]. `-frames:v 1` в этой команде добавлять нельзя: получите первый кадр после перемотки, а не последний. Для «продолжить видео» сохраняйте кадр в PNG, чтобы не копить JPEG-артефакты между поколениями. У некоторых провайдеров ролик заканчивается затуханием, тогда последний кадр чёрный: берите кадр за 0.2-0.5 с до конца.

Живые фото (Bot API 10.0, 8 мая 2026) [док] core.telegram.org/bots/api-changelog: `sendLivePhoto` принимает `live_photo` (видео до 10 с и 10 МБ) и `photo` (неподвижный кадр), по URL отправить нельзя, поддерживаются в `sendMediaGroup` и `editMessageMedia`. Требования к кодеку видео не указаны [?]. Для image-to-video из 3-5 секунд делайте: первый кадр в `photo`, клип в `live_photo`, ремукс по схеме выше.

## 4. Модерация

### 4.1 OpenAI omni-moderation

[док] developers.openai.com/api/docs/guides/moderation:
- Модель `omni-moderation-latest`, endpoint бесплатный. Принимает текст и изображения (URL или data URL), картинка до 20 МБ. Число картинок в запросе не указано [?].
- Категории с поддержкой изображений: `sexual`, `violence`, `violence/graphic`, `self-harm`, `self-harm/intent`, `self-harm/instructions`. Остальные (`harassment`, `harassment/threatening`, `hate`, `hate/threatening`, `illicit`, `illicit/violent`, `sexual/minors`) работают только по тексту. Для картинки без текста их оценка 0. То есть `sexual/minors` по фото не проверяется.
- Ответ: `flagged`, `categories`, `category_scores`, `category_applied_input_types`.
- Не отправляйте туда известный или предполагаемый CSAM.

Можно ли гонять нашим ключом проверку для бесплатного тарифа: в текущей документации сказано просто «free to use». Раньше FAQ OpenAI ограничивал бесплатное использование мониторингом входов и выходов OpenAI API, остальные случаи запрещались (цитируется на community.openai.com/t/can-i-use-open-ai-moderation-api-agaist-non-open-ai-content-part-2/673566). Позже формулировку смягчили, официального ответа нет. Вывод: для задач, которые всё равно уходят в OpenAI, использовать можно. Для трафика Gemini и OpenRouter это серая зона, и я бы либо написал в поддержку OpenAI, либо принял риск осознанно. Лимиты запросов на moderation endpoint я не нашёл [?].

### 4.2 Альтернативы

| Вариант | Что известно |
|---|---|
| Фильтры самих провайдеров | Работают всегда, см. 4.3. Вход фильтруют не все |
| Google Cloud Vision SafeSearch | ~1000 единиц в месяц бесплатно, дальше $1.50 за 1000, по локализованной копии прайса; на английской странице не перепроверял [?] |
| Azure AI Content Safety | Есть free-tier F0 с жёстким лимитом, цифры не подтвердил [?] |
| Локальный классификатор (NudeNet, ViT NSFW) | Без квот, нужно хостить и оценивать качество самим [?] |
| Cloudflare CSAM Scanning Tool | Сверяет кэшируемый контент с хэш-списками NCMEC, включается в Caching - Configuration, письма с совпадениями раз в сутки. Доступность по планам в документации не названа [?]. developers.cloudflare.com/cache/reference/csam-scanning/ |

### 4.3 Как провайдеры сообщают об отказе

| Провайдер | Признак | Что сказать пользователю |
|---|---|---|
| OpenAI images | Ошибка `type=image_generation_user_error`, `code=moderation_blocked`. В `moderation_details`: `moderation_stage` (`input`, `output`, `unknown`) и грубые `categories`. Автоповтор запрещён без правки промпта или входных фото | «Запрос отклонён модерацией OpenAI на этапе входа/результата, категория такая-то». Не списывать кредит |
| Gemini | Промпт заблокирован: ответ без кандидатов с `promptFeedback.blockReason` ∈ `SAFETY`, `OTHER`, `BLOCKLIST`, `PROHIBITED_CONTENT`, `IMAGE_SAFETY`. Результат заблокирован: `candidates[].finishReason` ∈ `SAFETY`, `PROHIBITED_CONTENT`, `BLOCKLIST`, `SPII`, `RECITATION`, `IMAGE_SAFETY`, `IMAGE_PROHIBITED_CONTENT`, `IMAGE_OTHER`, `IMAGE_RECITATION`, `NO_IMAGE` | `NO_IMAGE` значит, что модель вернула текст без картинки (иногда это словесный отказ): показать текст. Остальное - модерация. HTTP при этом 200 |
| OpenRouter chat/image | 403: «moderation flag» с `error.metadata.reasons`, `flagged_input` (до 100 символов), `provider_name`, `model_slug`; также 403 от guardrail. `finish_reason: content_filter` и `message.refusal` на 200. Images API: 502 при незавершённой генерации, без списания | Различать «отклонено модерацией» и «сбой провайдера» |
| OpenRouter video | Задача `failed` с полем `error` (в примерах - нарушение content policy), вебхук `video.generation.failed` | Показать `error.message` |

Источники: developers.openai.com/api/docs/guides/image-generation, ai.google.dev/api/generate-content, openrouter.ai/docs/api/reference/errors-and-debugging, openrouter.ai/docs/features/multimodal/image-generation, openrouter.ai/docs/features/multimodal/video-generation. Важное замечание из доки OpenRouter: ошибки, возникшие во время генерации, приходят с HTTP 200 в теле или в SSE-событии, поэтому разбирать тело надо даже при 200.

## 5. Очередь на Postgres

Паттерн из документации PostgreSQL 17 (postgresql.org/docs/17/sql-select.html): `FOR UPDATE SKIP LOCKED` пропускает залоченные строки и подходит для очередей, но даёт непротиворечивое представление данных не больше, чем нужно очереди. `NOTIFY` (postgresql.org/docs/17/sql-notify.html): уведомление уходит после коммита, payload короче 8000 байт, одинаковые payload в одной транзакции схлопываются, очередь уведомлений ограничена 8 ГБ, долгая транзакция слушателя блокирует очистку. Документация не говорит, что будет с уведомлениями при разрыве соединения слушателя; на практике они теряются [?], поэтому поллинг раз в 5-10 с обязателен как страховка.

Для генерации картинок и видео классический «лок на время обработки» не подходит: задача часами ждёт провайдера, и держать транзакцию нельзя. Нужна аренда:

```sql
-- захват пачки с арендой на 2 минуты
WITH c AS (
  SELECT id FROM framy.jobs
  WHERE state='queued' AND run_at <= now()
  ORDER BY run_at LIMIT $1 FOR UPDATE SKIP LOCKED)
UPDATE framy.jobs j
SET state='running', attempt=attempt+1, locked_by=$2, locked_until=now()+interval '2 minutes'
FROM c WHERE j.id=c.id RETURNING j.*;
-- при вставке в той же транзакции: SELECT pg_notify('framy_jobs', id::text);
-- reaper: UPDATE ... SET state='queued' WHERE state='running' AND locked_until < now();
```

Воркер продлевает аренду, а `provider_job_id` пишет в БД сразу после отправки платного запроса. Иначе ретрай после падения отправит оплаченную генерацию второй раз. Это относится и к собственной очереди, и к River.

River (github.com/riverqueue/river):
- Версия v0.49.0 от 5 октября 2026, до v1, лицензия MPL-2.0 (файловое копилефт-условие, как зависимость без правок не мешает) [док pkg.go.dev].
- Есть в OSS: ретраи, запланированные и периодические задачи, уникальные задачи, несколько очередей с `MaxWorkers`, веб-UI, `InsertTx` (задача появляется атомарно с вашей записью), LISTEN/NOTIFY, `JobSnooze` (перенос задачи без траты попыток - удобно для поллинга видео) [док]. riverqueue.com/docs, riverqueue.com/pro.
- Только в Pro ($125 в месяц): workflows, batching, sequences, concurrency limits, dead letter queue, durable periodic jobs. Лимит параллельных задач на пользователя придётся делать самим (счётчик активных задач перед вставкой) или через отдельные очереди по провайдерам.
- Схема: `river.Config{Schema: "framy"}` и `rivermigrate.Config{Schema: "framy"}`, это и есть рекомендуемый способ вместо `search_path` (riverqueue.com/docs/alternate-schema).
- Подключение: клиент, который берёт задачи, должен идти в Postgres напрямую или через session-пул, не через PgBouncer в transaction-режиме, потому что LISTEN/NOTIFY требует настоящей сессии (riverqueue.com/docs/pgbouncer). Выясните, стоит ли перед core-postgres пулер.

Что посоветую: River OSS. Ретраи с backoff, спасение зависших задач, `JobSnooze` для поллинга, `InsertTx` и уникальность по `update_id` Telegram (дедупликация повторных апдейтов) вы иначе будете писать сами. Две оговорки: пре-1.0 API, и одна пользовательская очередь вам не нужна. Свой вариант по схеме выше оправдан, если задача одна, зависимостей нужно ноль, и вы готовы поддерживать reaper и backoff. Очереди и лимиты воркеров: `openai`, `gemini`, `openrouter_video`, `ffmpeg` (низкий лимит, CPU), `telegram_send`.

Прогресс для SSE: отдельный `pg_notify('framy_events', '{"job":…,"user":…}')` из той же транзакции, что меняет состояние. Один слушатель на процесс на выделенном соединении (pgx `WaitForNotification`) раздаёт события подписчикам в памяти. После переподключения слушателя делайте разовое чтение состояния из БД.

## 6. Telegram Login (OIDC) и шифрование ключей

### 6.1 OIDC

Проверено живым запросом к `https://oauth.telegram.org/.well-known/openid-configuration` [тест]:
- issuer `https://oauth.telegram.org`, `authorization_endpoint /auth`, `token_endpoint /token`, `jwks_uri /.well-known/jwks.json`. `userinfo_endpoint` нет.
- `token_endpoint_auth_methods_supported`: `client_secret_basic`, `client_secret_post` (в документации заявлен только Basic).
- `grant_types_supported`: только `authorization_code`. Refresh-токена нет: сессию ведёте вы.
- `id_token_signing_alg_values_supported`: RS256, ES256, EdDSA, ES256K. По умолчанию RS256, меняется в BotFather.
- PKCE `S256` и `plain`.
- В JWKS четыре ключа (RSA, EC, OKP, ES256K).
- Scopes в документации (core.telegram.org/bots/telegram-login): `openid`, `profile` (даёт `id`, `name`, `preferred_username`, `picture`), `phone`, `telegram:bot_access` (бот может писать пользователю после логина). Redirect URI регистрируются в BotFather. ID-токен живёт 3600 с.

Подходят ли `coreos/go-oidc` + `x/oauth2`: да. `oidc.NewProvider(ctx, "https://oauth.telegram.org")` отработал на реальном discovery, userinfo пуст и не вызывается [тест]. Версии: go-oidc v3.21.0, x/oauth2 v0.37.0 (в моей сборке). Уточнения:
- `Endpoint().AuthStyle` приходит как autodetect, поставьте `oauth2.AuthStyleInHeader` явно.
- `Verify` не проверяет nonce, это ваша обязанность (док go-oidc).
- Задайте `SupportedSigningAlgs: []string{"RS256"}`: по умолчанию вердикт берётся из списка провайдера, где есть ES256K.
- PKCE: `oauth2.GenerateVerifier()`, `S256ChallengeOption`, `VerifierOption` есть в x/oauth2 с v0.13 [док].
- Для связи сайта и бота нужен числовой Telegram ID: берите claim `id` (scope `profile`). Совпадает ли `sub` с ним, не проверял [?].

```go
prov, _ := oidc.NewProvider(ctx, "https://oauth.telegram.org")
ep := prov.Endpoint(); ep.AuthStyle = oauth2.AuthStyleInHeader
cfg := &oauth2.Config{ClientID: id, ClientSecret: secret, Endpoint: ep,
    RedirectURL: "https://framy.ws04.cc/auth/callback",
    Scopes: []string{oidc.ScopeOpenID, "profile", "telegram:bot_access"}}
ver := oauth2.GenerateVerifier()           // хранить рядом с state и nonce
u := cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(ver), oidc.Nonce(nonce))
// callback: tok, _ := cfg.Exchange(ctx, code, oauth2.VerifierOption(ver))
// raw := tok.Extra("id_token").(string)
// idt, _ := prov.Verifier(&oidc.Config{ClientID: id, SupportedSigningAlgs: []string{"RS256"}}).Verify(ctx, raw)
// проверить idt.Nonce == nonce, обязательно сверить state
```

Часть до `AuthCodeURL` я собрал и запустил [тест]. `Exchange` и `Verify` не прогонял, нужен реальный бот.

Используйте серверный redirect-флоу, а не popup-библиотеку `telegram-login.js`. Popup требует, чтобы сайт не отдавал `Cross-Origin-Opener-Policy: same-origin` (допустим `same-origin-allow-popups`), иначе связь с окном рвётся (core.telegram.org/bots/telegram-login). Серверный флоу вообще снимает эту проблему. Внутри Mini App OIDC не нужен: там `initData` (п. 8).

### 6.2 Шифрование ключей пользователей

tink-go v2.9.0 (15 октября 2026, Apache-2.0) умеет envelope-шифрование: `aead.NewKMSEnvelopeAEAD2(dekTemplate, keyEncryptionAEAD)`, но документация помечает `NewKMSEnvelopeAEADWithContext` как предпочтительный и показывает только fake KMS «для тестов». Рецепта с локальным KEK из env там нет (pkg.go.dev/github.com/tink-crypto/tink-go/v2/aead). У пакета 29 импортов.

Стандартная библиотека проще и надёжнее для вашей задачи. Начиная с Go 1.24 есть `cipher.NewGCMWithRandomNonce`: nonce генерируется сам и кладётся в начало шифртекста, накладные расходы 28 байт, один ключ нельзя использовать больше чем для 2^32 сообщений (pkg.go.dev/crypto/cipher). С отдельным DEK на запись лимит вас не касается.

```go
func seal(key, aad, pt []byte) []byte {
    b, _ := aes.NewCipher(key)                 // 32 байта
    g, _ := cipher.NewGCMWithRandomNonce(b)
    return g.Seal(nil, nil, pt, aad)           // nonce nil: он внутри
}
// запись: dek=rand32; wrapped=seal(kek, aad, dek); ct=seal(dek, aad, apiKey)
// aad = user_id|provider|key_id|kek_version
```

Схема таблицы: `kek_version int`, `dek_wrapped bytea`, `ciphertext bytea`, `key_hint text` (последние 4 символа). Ротация KEK - перешифровать только DEK. AAD привязывает шифртекст к пользователю и версии. Дополнительно:
- Переменные окружения видны через `docker inspect` и `/proc/<pid>/environ`. Лучше файл-секрет с правами 0400 и паттерн `FRAMY_KEK_FILE`, чем `FRAMY_KEK` в env.
- KEK храните отдельно от бэкапов БД (менеджер паролей). Потерян KEK - потеряны все ключи пользователей.
- Ключ после сохранения клиенту не возвращать. На сохранении делать дешёвый тестовый запрос. Ключи не писать в логи, заголовок `Authorization` вычищать.

## 7. Вебхуки OpenRouter для видео

[док] openrouter.ai/docs/cookbook/video-generation/video-generation-webhooks и openrouter.ai/docs/features/multimodal/video-generation:
- `POST /api/v1/videos` принимает `callback_url` (только HTTPS). Он переопределяет значение по умолчанию на уровне workspace. Ответ 202 с `id`, `polling_url`, `status: pending`. Статусы `pending`, `in_progress`, `completed`, `failed`. Рекомендованный интервал поллинга около 30 с.
- События: `video.generation.completed`, `.failed`, `.cancelled`, `.expired`. Тело: `type`, `created_at`, `data` (`id`, `status`, `generation_id`, `model`, `unsigned_urls`, `usage.cost`, `usage.is_byok`, `error`). `generation_id` и `model` могут быть null при раннем отказе.
- Заголовки: `X-OpenRouter-Signature: t=<unix-секунды>,v1=<hex>` и `X-OpenRouter-Idempotency-Key: <job_id>-<status>`.
- Проверка: взять сырое тело до JSON-разбора; подписываемая строка `t + "," + тело`; HMAC-SHA256 с секретом workspace, hex; сравнение в постоянное время; отвергать, если `|now - t| > 300` секунд. Ответы: 204 на валидное, 401 на неверную подпись. Секунды подтверждены по коду примера (`Date.now()/1000`).
- Политика повторов и таймаут доставки в документации не описаны [?]. Idempotency-ключ намекает, что повторы бывают.
- `unsigned_urls` - это не presigned: скачивать надо с `Authorization: Bearer <ключ>`. Если есть сохранённая копия, в ответе может быть и `urls` с подписью и `expires_at`, их считайте секретом.

Ключевой вопрос про ключи пользователей. Секрет подписи настраивается в настройках workspace (в документации не сказано, кто имеет к нему доступ). Подпись присутствует только когда секрет задан. Ключ, полученный через OAuth, привязан к аккаунту пользователя (в OAuth-запросе можно предвыбрать `workspace_id`) (openrouter.ai/docs/guides/overview/auth/oauth), а секрет вы у него не знаете. Ограничений на `callback_url` по типу ключа в документации не нашёл, но и прямого подтверждения, что он работает с пользовательскими ключами, нет [?]. Проверьте на тестовом OAuth-ключе. Соответственно:
- Для своих ключей: проверка подписи как выше.
- Для ключей пользователей: вебхук считайте только сигналом. В `callback_url` положите неугадываемый путь `/hooks/or/<job_uuid>/<hmac(job_uuid)>`, при получении перечитайте статус через `polling_url` пользовательским ключом и действуйте по этому ответу. Поллинг раз в 30 с оставляйте как фолбэк: вебхук может не прийти.
- Идемпотентность: таблица `webhook_events(idempotency_key unique)`, `INSERT … ON CONFLICT DO NOTHING`, тяжёлую работу (скачивание, ffmpeg) выполнять в очереди, отвечать быстро. Вебхук может прийти после того, как поллинг уже закрыл задачу.

```go
func verifyOR(secret, hdr string, body []byte, now time.Time) bool {
    var ts, v1 string
    for _, p := range strings.Split(hdr, ",") {
        k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
        if k == "t" { ts = v } else if k == "v1" { v1 = v }
    }
    sec, err := strconv.ParseInt(ts, 10, 64)
    want, err2 := hex.DecodeString(v1)
    if err != nil || err2 != nil { return false }
    if d := now.Unix() - sec; d > 300 || d < -300 { return false }
    m := hmac.New(sha256.New, []byte(secret)); m.Write([]byte(ts + ",")); m.Write(body)
    return hmac.Equal(m.Sum(nil), want)
}
```

Публичный HTTPS-эндпоинт через Tunnel подходит, но Bot Fight Mode (п. 8) может задерживать запросы не из браузера.

## 8. Что обычно забывают

### 8.1 Cloudflare Free и бот-защита

| Что | Факт |
|---|---|
| WAF custom rules | 5 штук, Skip доступен [док] developers.cloudflare.com/waf/custom-rules/ |
| Rate limiting | 1 правило, окно 10 с, только по IP [док] developers.cloudflare.com/waf/rate-limiting-rules/ |
| Bot Fight Mode | Нельзя обойти ни WAF-правилами, ни Skip. Работает вне движка правил и может челленджить API и мобильные клиенты. Исключение - IP Access rule [док] developers.cloudflare.com/bots/get-started/bot-fight-mode/ |
| TelegramBot (превью ссылок) | В списке verified bots Cloudflare (UA `TelegramBot (like TwitterBot)`): radar.cloudflare.com/bots/directory/telegrambot |
| Реальный IP | Заголовок `CF-Connecting-IP`. `True-Client-IP` только Enterprise: developers.cloudflare.com/fundamentals/reference/http-headers/ |

Практически: Bot Fight Mode для hostname лучше выключить или проверить, что POST от OpenRouter и ваши API-клиенты проходят. Либо держать поллинг как основной путь. Бот работает по long polling к локальному Bot API серверу, входящих вебхуков от Telegram нет.

`CF-Connecting-IP` доверяйте, только если порт приложения доступен исключительно из сети cloudflared (никаких `ports:` на хост у сервиса).

### 8.2 Rate limiting в приложении

Cloudflare даёт одно правило, поэтому остальное в коде:
- Токен-бакет на пользователя и на IP (`golang.org/x/time/rate`, один процесс - памяти хватит). Строже на `/auth/*`, загрузки, создание задач.
- Суточные и месячные квоты на кредиты - в Postgres, а не в памяти.
- Лимит активных задач на пользователя, лимиты воркеров на провайдера (очереди River).
- Лимит одновременных SSE на пользователя (например 3), `ReadHeaderTimeout`, `MaxHeaderBytes`.
- Исходящие лимиты Telegram [док] core.telegram.org/bots/faq: не больше 1 сообщения в секунду в чат, 20 в минуту в группу, около 30 в секунду всего, сверх этого 429.

### 8.3 CSRF и сессии

- Go 1.25+ имеет `http.CrossOriginProtection` (по умолчанию проверяет `Sec-Fetch-Site`, при его отсутствии сверяет `Origin` и `Host`; GET, HEAD, OPTIONS пропускает; запросы без обоих заголовков считает не-браузерными и пропускает): pkg.go.dev/net/http#CrossOriginProtection. Обернуть mux, добавить `AddInsecureBypassPattern` для `/hooks/…`. Токены CSRF для JSON-API не нужны. На GET нельзя менять состояние.
- Куки сайта: имя `__Host-…`, `HttpOnly; Secure; SameSite=Lax; Path=/`, в БД хранить хэш идентификатора сессии.
- Mini App в iframe на web.telegram.org: куки в стороннем контексте ненадёжны (политики браузеров, в том числе у Telegram есть баг про сессию в iframe: bugs.telegram.org/c/37071) [?]. Поэтому для Mini App: `initData` на сервер, сервер выдаёт короткоживущий bearer-токен, клиент шлёт его в заголовке. Bearer-запросы от CSRF не страдают, но нужны фетч-SSE или ticket (п. 1.2).
- Проверка `initData` [док] core.telegram.org/bots/webapps: HMAC-SHA-256, секрет = HMAC(`WebAppData`, токен бота); строка проверки - все поля, кроме `hash`, по алфавиту через `\n`; проверять `auth_date` на свежесть (я бы брал не более часа). Есть и Ed25519-проверка `signature` без токена бота.

### 8.4 CSP и заголовки (сайт и Mini App на одном origin)

Telegram Web отдаёт `X-Frame-Options: deny` на себя (проверил curl'ом), а в iframe показывает ваш Mini App. Хосты Telegram Web, найденные мной: `web.telegram.org` (`/k/`, `/a/`), `webk.telegram.org`, `weba.telegram.org` (сюда редиректит `webz.telegram.org`) [тест]. Официального списка origin для `frame-ancestors` в документации нет [?]. Нативные клиенты используют webview, `frame-ancestors` к ним не относится.

```
Content-Security-Policy:
  default-src 'none';
  script-src 'self' 'nonce-<N>' https://telegram.org;
  style-src 'self' 'nonce-<N>';
  img-src 'self' data: blob: https://framy-media.ws04.cc https://cdn4.telesco.pe;
  media-src 'self' blob: https://framy-media.ws04.cc;
  connect-src 'self' https://<ACCOUNT_ID>.r2.cloudflarestorage.com;
  font-src 'self'; base-uri 'none'; object-src 'none'; form-action 'self';
  frame-ancestors https://web.telegram.org https://webk.telegram.org https://weba.telegram.org
Cross-Origin-Opener-Policy: same-origin-allow-popups   (если останется popup-логин, иначе same-origin)
Referrer-Policy: no-referrer           (на страницах с токеном в URL)
Strict-Transport-Security: max-age=31536000
```

Пояснения:
- `X-Frame-Options` на сайте не ставьте: он конфликтует с `frame-ancestors`, который нужен Telegram.
- Один набор `frame-ancestors` на все HTML-маршруты проще, чем разные по путям: фреймить смогут только origin Telegram.
- `cdn4.telesco.pe` встретился в примере ID-токена как `picture` и аватарки пользователей; остальные хосты аватарок не проверял [?].
- Cloudflare может инжектировать скрипты (Rocket Loader, Web Analytics, обфускация email): проверьте настройки, иначе CSP их заблокирует [?].
- Bot API 10.2 (14 июля 2026), автоматически с 20 июля 2026: методы Mini App не работают, если вызываются с origin, отличного от исходного домена Mini App; отключается в BotFather. Не уводите Mini App на сторонние сайты и не вызывайте `Telegram.WebApp.*` из чужих фреймов. core.telegram.org/bots/api-changelog.

### 8.5 CORS для `WebApp.downloadFile`

[док] core.telegram.org/bots/webapps: `downloadFile({url, file_name})` (Bot API 8.0) показывает нативный диалог. `url` должен быть HTTPS. В ответе сервера должны быть `Content-Disposition: attachment; filename="…"` и `Access-Control-Allow-Origin: https://web.telegram.org`, иначе в веб-версии может не работать. Заголовок допускает одно значение: отражайте `Origin` из белого списка (web, webk, weba) и ставьте `Vary: Origin`. Для файлов из R2: CORS-политика бакета отдаёт заголовки на кастомном домене автоматически (developers.cloudflare.com/r2/buckets/cors/), а `Content-Disposition` можно выставить метаданными объекта при загрузке или через `response-content-disposition` в presigned GET. Следует ли `downloadFile` за редиректами с `302` на presigned URL, не знаю [?], тестируйте на реальных клиентах.

### 8.6 Публичные ссылки на результаты: robots и OG

- Токен ссылки - 128 бит случайных, возможность отзыва, срок жизни, `Referrer-Policy: no-referrer`.
- На странице `<meta name="robots" content="noindex">` и `X-Robots-Tag: noindex`. `Disallow` в robots.txt для страниц не используйте: краулер не увидит noindex, а блокировка может зацепить превью-бота.
- OG-теги: `og:title`, `og:description`, `og:image` (абсолютный https, на R2-домене), `og:image:width/height`, для видео `og:video` и `og:video:type=video/mp4`, `twitter:card`. Telegram берёт превью сам, UA `TelegramBot (like TwitterBot)` (Radar). Подробностей по тегам в документации Telegram не нашёл [?]. Обновление кэша превью традиционно делают через @WebpageBot, это знание не из документации [?].
- Результаты генеративных моделей: решите заранее, оставляете ли вы метаданные провайдера (например C2PA, SynthID) и маркировку. Я не проверял правовые требования [?].

### 8.7 Бэкапы

- `pg_dump -Fc -n framy` ежедневно в отдельный бакет R2 (`framy-backups`, lifecycle 30 дней, шифрование `age`), плюс WAL-архивация, если нужна точка восстановления. Роли в `pg_dump` схемы не входят, держите скрипт их создания в репозитории. Раз в месяц тест восстановления в чистую БД.
- KEK отдельно от дампов и от кода. Без него зашифрованные ключи бесполезны.
- Объекты R2 для результатов не бэкапьте, если у них есть срок жизни. Если «галерея навсегда» - нужна политика хранения и бэкап.
- Том telegram-bot-api: сколько файлов он копит на диске и чистятся ли они, не проверял [?]. Заведите задачу уборки по возрасту и мониторинг места.

### 8.8 Метрики и стоимость

- В таблицу задач: `provider`, `model`, `byok bool`, `cost_usd numeric`, `input_tokens`, `output_tokens`, `pricing_snapshot jsonb`. Цены меняются, фиксируйте снимок на момент задачи.
- Источники стоимости: OpenRouter - `usage.cost` и `usage.is_byok` в ответе на опрос видео и в вебхуке, суммарные `usage`, `usage_daily`, `limit_remaining` через `GET /api/v1/key` (openrouter.ai/docs/api/reference/limits); OpenAI и Gemini - токены из `usage` / `usageMetadata`, цена по вашей таблице.
- Prometheus: `framy_job_cost_usd_total{provider,model,byok}`, `framy_job_duration_seconds`, `framy_job_failures_total{reason}` (отдельные причины: `moderation`, `provider_error`, `timeout`, `quota`), длина очереди, число SSE. `/metrics` только на внутреннем порту.
- Предохранитель: суточный потолок расходов на наши ключи по провайдеру, при превышении закрывать бесплатный тариф. Алерт на `limit_remaining` ключа OpenRouter.
- Не забыть: идемпотентность создания задач (ключ идемпотентности от клиента, `update_id` у бота), graceful shutdown (закрыть SSE, дождаться воркеров), удаление данных пользователя целиком (БД, R2, зашифрованные ключи), защита от SSRF при скачивании по URL (запрет приватных адресов в `net.Dialer.Control`), процедура на случай CSAM (удаление, сохранение записей, обязанность сообщить по вашей юрисдикции).

## Не проверено, проверить до запуска

- Доступность «Respect Existing Headers» для Browser Cache TTL на Free и поведение Browser TTL с `no-store`.
- HEIC с настоящих iPhone (Portrait, Live Photo, HDR) через ffmpeg в вашем образе, версия ffmpeg там.
- Работа `callback_url` OpenRouter с OAuth-ключами пользователей, наличие подписи в этом случае.
- Работа Bot Fight Mode с POST от OpenRouter.
- Воспроизведение итоговых MP4 и размеры/кодек для `sendLivePhoto` на iOS, Android, Desktop, Web.
- Допустимость omni-moderation для не-OpenAI трафика (формулировка менялась, офиц. ответа нет).
- Следует ли `WebApp.downloadFile` редиректам; полный список origin Telegram Web для `frame-ancestors`.
- Просит ли Cloudflare карту при включении R2.

## Выводы для framy

1. Видео и все большие файлы результатов отдавать из R2 с кастомного домена первого уровня (`framy-media.ws04.cc`), а не через Tunnel: Cloudflare прямо относит трафик публичных Tunnel-hostname на Free к ограничению на видео. Картинки результатов туда же.
2. Загрузки с сайта: фото до ~25 МБ через Go-сервис потоком на диск, видео-референсы через presigned PUT прямо в R2 (бакет с CORS, `incoming/` с lifecycle 1 сутки). После загрузки проверять размер через HEAD, содержимое - по магическим байтам и `ffprobe`. Помнить про лимит 100 МБ на тело запроса через Cloudflare.
3. Все долгие операции - асинхронные: POST создаёт задачу и отвечает сразу, прогресс по SSE (`text/event-stream`, flush после события, ping каждые 15-20 с, `WriteTimeout` снят для этого маршрута, `Last-Event-ID`), запасной путь - поллинг. WebSocket не нужен.
4. Кэш: `Cache-Control: no-store` на `/api`, `/auth`, `/events`, `/hooks`; `public, max-age=31536000, immutable` на файлы с хэшем и публичные картинки; Browser Cache TTL зоны поставить в «Respect Existing Headers» (или Cache Rule «Respect origin»); `public` никогда не ставить на авторизованный контент.
5. Cloudflare Free закрывает мало: 1 правило rate limiting, 5 WAF-правил, Bot Fight Mode нельзя обойти правилами. Лимиты на пользователя и IP, квоты и конкуренцию реализовать в приложении; реальный IP брать из `CF-Connecting-IP` только при недоступности порта приложения снаружи.
6. Нормализацию входных картинок делать ffmpeg-подпроцессом (HEIC, AVIF, WebP, JPEG, PNG): ffprobe-проверка размеров и формата, временный файл с вашим именем и `-protocol_whitelist file`, автоповорот, `-map_metadata -1`, длинная сторона ≤ 2048, JPEG (PNG при альфе). Запасной вариант - gen2brain/heic (MIT, без cgo), но он тяжелее: 0.75 с и 430 МБ на 12 Мпикс. Проверить HEIC в образе до релиза.
7. Для Telegram: `sendPhoto` только при ≤10 МБ и правилах сторон, иначе JPEG-превью плюс `sendDocument` с оригиналом. Отправку и скачивание делать через локальные пути на общем томе (`file://`). Альбомы собирать по `media_group_id` с задержкой.
8. Видео: `ffprobe`, затем ремукс `-c copy -movflags +faststart` для H.264/AAC yuv420p, иначе перекодирование в H.264 High yuv420p + AAC + faststart с чётными размерами. Передавать `width`, `height`, `duration` из ffprobe, `supports_streaming=true`, при нужном кадре - `cover`. Последний кадр: `ffmpeg -sseof -1 -i in.mp4 -update 1 out.png` (без `-frames:v`), сохранять PNG. Живые фото - клип ≤10 с и ≤10 МБ плюс кадр, после проверки на клиентах.
9. Модерация: для бесплатного тарифа предмодерация `omni-moderation-latest` (бесплатно) на промпт и входные картинки, помня, что `sexual/minors` и ряд категорий по картинке не считаются. Для трафика не в OpenAI это серая зона, решить осознанно. Для BYOK полагаться на фильтры провайдеров. Отказы разбирать по таблице п. 4.3 и всегда смотреть тело ответа, даже при HTTP 200; в интерфейсе различать «модерация» и «сбой».
10. Очередь: River OSS (v0.49.0, MPL-2.0) в схеме `framy` с `Schema` в конфиге, подключение напрямую к Postgres, очереди по провайдерам с `MaxWorkers`, `JobSnooze` для поллинга видео, `InsertTx` для атомарного создания. Лимит активных задач на пользователя сделать самим (в Pro платно). Состояние генерации и `provider_job_id` хранить в своей таблице и писать до начала поллинга, чтобы ретрай не оплатил запрос дважды.
11. Вход через Telegram: серверный authorization-code flow с PKCE S256, `coreos/go-oidc` + `x/oauth2` (проверено на реальном discovery), `AuthStyleInHeader`, `SupportedSigningAlgs: RS256`, ручная проверка nonce и state, scope `openid profile` (+ `telegram:bot_access`), связка по claim `id`. Свою сессию вести самим, refresh-токена нет. В Mini App - `initData` и короткий bearer-токен вместо куки.
12. Шифрование ключей: стандартная библиотека, AES-256-GCM через `cipher.NewGCMWithRandomNonce`, DEK на запись, AAD = пользователь|провайдер|версия KEK. KEK из файла-секрета, не из env, хранить отдельно от бэкапов. Tink не брать: локального KEK-рецепта в его документации нет.
13. OpenRouter-вебхуки: `callback_url` на каждый запрос видео с неугадываемым путём; для своих ключей проверять подпись (`t,v1`, HMAC-SHA256 по `t + "," + сырое тело`, окно 300 с), для ключей пользователей считать вебхук лишь сигналом и подтверждать статусом через `polling_url`. Дедупликация по `X-OpenRouter-Idempotency-Key`, поллинг раз в ~30 с оставить фолбэком. Проверить работу `callback_url` с OAuth-ключом на тестовом аккаунте.
14. Заголовки: единая CSP на все HTML-маршруты с `frame-ancestors` для `web.telegram.org`, `webk.telegram.org`, `weba.telegram.org`, без `X-Frame-Options`; CSRF через `http.CrossOriginProtection`; куки `__Host-` с `SameSite=Lax`; `no-referrer` на страницах с токенами; для `downloadFile` отражать `Origin` из белого списка и отдавать `Content-Disposition: attachment`.
15. Публичные ссылки: токены 128 бит, `noindex` вместо `Disallow`, OG-теги с абсолютными https-URL на домене R2, проверка превью через @WebpageBot после запуска. Включить в Cloudflare CSAM Scanning Tool, если доступно на плане.
16. Бэкапы: ежедневный `pg_dump -n framy` в R2 с шифрованием и сроком хранения, регулярная проверка восстановления, KEK отдельно. Уборка файлов telegram-bot-api по возрасту.
17. Стоимость: писать `cost_usd`, `byok`, токены и снимок цен в каждую задачу, метрики Prometheus по провайдерам и причинам отказов, суточный потолок расходов на наши ключи с автоотключением бесплатного тарифа.
18. Перед первым релизом пройти список «Не проверено» выше на тестовом стенде с реальными файлами с iPhone, настоящим OAuth-ключом OpenRouter и живыми клиентами Telegram.