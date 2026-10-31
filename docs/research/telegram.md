# Telegram для framy: Bot API 10.3, Mini Apps, вход через Telegram

Дата исследования: 2026-10-26. Язык: русский, имена методов как в API.

## 0. Источники, метки, версия

Страницы скачаны и разобраны локально 2026-10-26:

- https://core.telegram.org/bots/api-changelog (верхняя запись: Bot API 10.3, 24.08.2026; новее нет)
- https://core.telegram.org/bots/api
- https://core.telegram.org/bots/webapps (последняя запись про Mini App API: 10.1, 11.06.2026; в списке полей WebApp есть `Serverless` без даты и без отдельной документации)
- https://core.telegram.org/bots/features
- https://core.telegram.org/bots/faq
- https://core.telegram.org/bots/telegram-login
- https://core.telegram.org/bots/payments-stars
- https://oauth.telegram.org/.well-known/openid-configuration (запрошен живьём)
- Исходник локального сервера на нашем пине: https://github.com/tdlib/telegram-bot-api/blob/e3e9dd8e5b3d7ab8537cd5a10dc31d5ffa8f82d1/telegram-bot-api/Client.cpp

Метки в тексте:

- [док] сказано в официальной документации на 26.10.2026.
- [код] видно в исходнике Client.cpp на пине e3e9dd8 (наш `telegram-bot-api` v10.3-1).
- [память] знание из памяти или сообщества, на странице не нашёл.
- [не проверено] логический вывод или гипотеза, нужен живой тест (список тестов в разделе 10.4).

Мы на Bot API 10.3: `tg` README, `telegram-bot-api` README и CHANGELOG (v10.3-1) совпадают с текущей версией. Всё из 10.x на нашем сервере есть. Веб-поиск по теме альбомов и getMessage ничего полезного не дал (только про буферизацию альбомов по `media_group_id`), так что выводы по альбомам опираются на документацию и на исходник сервера.

## 1. Хронология Bot API, что из неё нужно framy

| Версия | Дата | Что важно для framy |
| --- | --- | --- |
| 7.0 | 29.12.2023 | `setMessageReaction` (бот ставит 1 реакцию), Replies 2.0 (`external_reply`, `quote`), `deleteMessages` |
| 7.4 | 28.05.2024 | `message_effect_id` (только личные чаты), `show_caption_above_media`, `expandable_blockquote`, платежи Stars (`XTR`), `refundStarPayment` |
| 7.6 | 01.07.2024 | платное медиа `sendPaidMedia`, `MenuButtonWebApp` по t.me-ссылке, Direct Link Mini App открываются на всю высоту |
| 7.8 | 31.07.2024 | Main Mini App, `WebApp.shareToStory` |
| 7.11 | 31.10.2024 | `copy_text` кнопка; `editMessageMedia` умеет заменить текстовое сообщение медиа |
| 8.0 | 17.11.2024 | fullscreen, safe area, home screen, `savePreparedInlineMessage` + `WebApp.shareMessage`, `WebApp.downloadFile`, emoji status, геолокация, сенсоры, подписки Stars (`subscription_period`); предупреждение про видео с `message_id` = 0 |
| 9.0 | 11.04.2025 | `DeviceStorage`, `SecureStorage` в Mini App; business-аккаунты |
| 9.3 | 31.12.2025 | `sendMessageDraft` (стриминг, только личные чаты), topics в личных чатах |
| 9.4 | 09.02.2026 | `style` кнопок (primary/success/danger), `icon_custom_emoji_id`, `createForumTopic` в личных чатах |
| 9.5 | 01.03.2026 | `sendMessageDraft` для всех ботов, entity `date_time`, `BottomButton.iconCustomEmojiId` |
| 9.6 | 03.04.2026 | managed bots, `savePreparedKeyboardButton`, `WebApp.requestChat` |
| 10.0 | 08.05.2026 | guest mode (`guest_message`, `answerGuestQuery`), live photos (`sendLivePhoto`), bot-to-bot, `deleteMessageReaction` |
| 10.1 | 11.06.2026 | rich messages (`sendRichMessage`, `sendRichMessageDraft`, `rich_message` в `editMessageText`), `InputRichMessageContent` в inline/guest/WebApp-ответах |
| 10.2 | 14.07.2026 | ephemeral messages и ephemeral commands, communities, `BotSubscriptionUpdated`; защита Mini App по origin (включена для всех с 20.07.2026) |
| 10.3 | 24.08.2026 | `EphemeralMessageParameters` (с `replace_callback_query_message`), `RichMessageButton` и `<tg-button>`, `DisabledButton`, `force_reply` в `InlineKeyboardMarkup`, `can_stop`/`keep_on_stop` + `MessageGenerationStopped`, загрузка файлов в `editEphemeralMessageMedia` |

Отдельно: Bot API 6.8 увеличил `startapp` в direct link до 512 символов (допустимы A-Z a-z 0-9 _ -). Для обычного deep link `?start=` остаётся 64 символа [док].

## 2. Сценарий A: альбом (главный технический вопрос)

### 2.1 Что боту приходит

- Каждое фото альбома приходит отдельным `update.message`. У всех одинаковый `media_group_id` (строка, уникальна внутри чата). Подпись обычно только у одного элемента. Поля "сколько элементов" и "список соседей" нет [док: Message.media_group_id].
- `reply_to_message` содержит ровно одно сообщение: то, на которое ответили. Внутри него есть `media_group_id`, `photo[]`, `caption`, но нет вложенного `reply_to_message` [док]. Если пользователь ответил на фото из альбома, это конкретный элемент (тот, на который он нажал).
- `external_reply` (ответ из другого чата или топика) тоже одно сообщение. `chat` и `message_id` в нём только для супергрупп и каналов. `quote` это кусок текста. Про соседей по альбому там ничего нет [док].
- `forward_origin` информации об альбоме не несёт.
- Две ловушки при разборе вложений: у сообщения с `animation` дополнительно заполнено `document`, у `live_photo` дополнительно заполнено `photo` (оба для обратной совместимости) [док]. Фото может прийти и как `document` с `image/*` (альбомы документов однородны).

### 2.2 Privacy mode

По https://core.telegram.org/bots/features#privacy-mode [док]. Бот с privacy mode (включён по умолчанию) в группе получает:

- команды, адресованные ему (`/f@framybot`);
- общие команды (`/f`), если он последним из ботов писал в эту группу;
- inline-сообщения, отправленные через него;
- ответы на сообщения, которые явно или неявно адресованы ему;
- всегда: сервисные сообщения, всё из приватных чатов, всё из каналов, где он участник.

Бот-администратор группы получает все сообщения. Privacy mode можно выключить в BotFather (глобально для бота, бота нужно заново добавить в группы). Документация сама отговаривает так делать и советует `force_reply` для сообщений бота.

Следствия:

- С включённым privacy mode остальные фото альбома боту не приходят вообще. Если команда стоит в подписи одного элемента альбома, придёт только он.
- Нового в 9.x-10.x, что расширяло бы видимость бота в группе, нет. Исключение: bot-to-bot (10.0) для сообщений других ботов, к альбомам не относится.
- Замечание про UX: при нескольких ботах в группе голая `/f` приходит только если framy писал в группу последним. Клиент подставляет `@framybot` при выборе из меню команд, значит команды нужно публиковать через `setMyCommands` (scope `all_group_chats`).

### 2.3 getMessage нет

Полный список методов `get*` в API на 10.3: getUpdates, getWebhookInfo, getMe, getUserProfilePhotos, getUserProfileAudios, getFile, getChat, getChatAdministrators, getChatMemberCount, getChatMember, getUserPersonalChatMessages, getForumTopicIconStickers, getUserChatBoosts, getBusinessConnection, getManagedBotToken, getManagedBotAccessSettings, getMyCommands, getMyName, getMyDescription, getMyShortDescription, getChatMenuButton, getMyDefaultAdministratorRights, getAvailableGifts, getBusinessAccountStarBalance, getBusinessAccountGifts, getUserGifts, getChatGifts, getStickerSet, getCustomEmojiStickers, getMyStarBalance, getStarTransactions, getGameHighScores. Метода получения сообщения по id нет.

`getUserPersonalChatMessages` (10.0) не подходит: возвращает 1-20 последних сообщений из личного чата, прикреплённого к профилю пользователя, а не из группы. `getChat` отдаёт только закреплённое сообщение.

### 2.4 forwardMessage, copyMessage и что сервер умеет внутри [док + код]

- `copyMessage`, `copyMessages` и `forwardMessages` возвращают только `MessageId` [док]. Бот не получает updates о собственных сообщениях, поэтому прочитать содержимое копии нельзя.
- `forwardMessage` (единичный) возвращает полный `Message`: `photo` с `file_id`, `caption`, `forward_origin` с оригинальной `date` [док].
- `forwardMessages` сохраняет склейку альбома, но отдаёт только id [док].
- Не пересылаются сообщения с `has_protected_content` [док].
- [код] `process_forward_message_query` вызывает `check_message(...)`, а тот шлёт в TDLib `td_api::getMessage(chat_id, message_id)`. `forwardMessages` идёт через `td_api::getMessages`. Доступ определяет `have_message_access`: для приватных и обычных групп всегда true, для супергруппы бот должен быть участником. То есть сервер умеет загрузить любое сообщение по id в чате, где бот состоит, независимо от privacy mode (privacy mode фильтрует updates, а не выборку по id). В API это наружу не выставлено. Так работает и подгрузка `reply_to_message` для апдейтов (строки с `getMessage` в Client.cpp).
- [код] `media_group_id` в JSON выставляется только если `media_album_id != 0`. После одиночной пересылки TDLib, скорее всего, обнуляет album id [не проверено].

### 2.5 Варианты решения

| Вариант | Как работает | Гарантии | Плюсы | Минусы |
| --- | --- | --- | --- | --- |
| A. Кэш по `media_group_id` (бот админ группы или privacy off) | Бот видит все сообщения, на каждое медиа пишет в БД (chat_id, media_group_id, message_id, file_id, file_unique_id, размеры, дата, автор). На `/f` с `reply_to_message.media_group_id` собирает всех соседей из БД | Точный состав альбома. По документации работает [док] | Не зависит от недокументированного. Ноль сетевых запросов на сборку | Нужны права. Бот хранит ссылки на медиа из чата. Только для альбомов, пришедших, пока бот был админом |
| B. Патч нашего `telegram-bot-api`: метод `getMessages(chat_id, message_ids)` | Мы собираем сервер сами из закреплённых исходников. Добавляем метод поверх `check_messages` (тот же `td_api::getMessages`, что уже используется для `forwardMessages`). Бот запрашивает id от M-9 до M+9 и фильтрует по `media_group_id` | Точный состав, без прав и без кэша [не проверено на живом боте: разрешит ли Telegram bot-аккаунту getMessages в режиме privacy] | Решает проблему целиком для любого чата. `tg.Preflight` уже умеет отказываться стартовать на сервере без метода | Свой патч C++ поверх BSL-1.0, надо переносить при каждом обновлении пина. Не работает на облачном api.telegram.org |
| C. Forward-probe | `forwardMessage(chat_id=SCRATCH, from_chat_id=group, message_id=i)` для i около M; из ответов берём `photo`, группируем по эвристике: тот же отправитель (`forward_origin`), та же `date`, подряд идущие id | Эвристика, не гарантия [не проверено]. Падает на защищённом контенте | Без прав, без патча, читает только то, что пользователь указал ответом | Мусор в scratch-чате (чистить `deleteMessages`), до 18 вызовов на команду, лимит 1 сообщение в секунду в личку, возможна нестыковка группировки |
| D. Ответ пользователю: "пришлите альбом в личку / загрузите на сайте / откройте Mini App" | В личном чате бот видит все элементы (privacy mode там не действует) | Работает всегда | Нулевая неопределённость | Ломает сценарий "ответил на альбом в группе" |
| E. Guest mode | Бот в чате не участник, получает `guest_message` с сообщением-триггером и "конкретным сообщением, на которое оно отвечало" [док: features#guest-bots] | Один ответный контекст, соседей нет | Работает в чатах без бота | Альбом не решает |

Что выбрал бы я: основной путь A, запасные B, затем C, и D как последний. Причины:

- A единственный целиком подтверждён документацией и даёт точный результат. Просить админа можно при первом `/f` по альбому, а не при установке бота.
- Не включать privacy off глобально: бот станет получать каждое сообщение каждой группы (нагрузка и репутация "читает всё"). Админ-статус выдаётся на конкретную группу.
- B стоит делать, если окажется, что админ в группах выдают неохотно. Мы всё равно владеем сервером, а код `forwardMessages` показывает, что нужный вызов TDLib там уже есть. Оценка объёма патча: порядка 40-60 строк C++, по образцу `process_forward_messages_query`.
- C нужен только как временная мера и только если B откладывается. Обязателен экран подтверждения, где видно, какие фото взяты (ваш сценарий "Да/Нет"): он превращает ошибку эвристики в решение пользователя.

### 2.6 Как собрать кэш (вариант A)

- Ключ: `(chat_id, media_group_id)`, значения: упорядоченный список элементов по `message_id`. Максимум элементов в альбоме 10 [док: sendMediaGroup 2-10].
- Источник данных: `message` и `edited_message` (в `allowed_updates` указать явно) с `photo`, `live_photo`, `animation`, `video`, `document` (image/*). Хранить `file_id`, `file_unique_id` (стабилен при ротации `file_id`), ширину, высоту, размер, дату, автора, подпись. Текст сообщений не хранить.
- TTL 48 часов (совпадает с окном `deleteMessage`), чистка по `my_chat_member` с `left`.
- Debounce: если команда пришла раньше чем через 1,5 с после последнего элемента альбома, подождать, пока альбом доедет.
- `reply_to_message` всегда включается в результат, даже если кэш пуст. Если у него есть `media_group_id`, а в кэше только он один, в превью пометить "вижу 1 фото; возможно, в альбоме их больше".
- Удалённые сообщения: боту в группах updates об удалении не приходят, удалённый элемент останется в кэше [не проверено, но событий об удалении в списке Update нет]. Принять как известное ограничение.
- Сообщения до того, как бот стал админом, в кэше отсутствуют. Для них работают B или C.
- Права: в `promoteChatMember` все права можно выключить, но принимает ли Telegram админа без единого права и приходит ли тогда поток всех сообщений, не проверял [не проверено]. Ссылка `t.me/framybot?startgroup&admin=...` задаёт набор прав.

### 2.7 Привязка к ответу на сообщение самого бота

Ответ пользователя (без команды) на сообщение бота приходит и с включённым privacy mode ("replies to messages meant for this bot") [док]. Поэтому "ответь на картинку текстом, чтобы доработать" работает без `/f` и без админства. Для сообщений бота хранить `(chat_id, message_id) -> generation_id`, чтобы источник брался из нашей БД, а не скачивался заново.

## 3. Сценарий B: красивые ответы

### 3.1 editMessageMedia: что во что превращается [док]

- Метод "edit animation, audio, document, live photo, photo, or video messages, or to replace a text or a rich message with a media" (7.11, дополнено 10.0 про live photo).
- Текст или rich -> медиа: да.
- Медиа -> текст: нет. `editMessageText` редактирует только text, rich и game сообщения. Чтобы вернуться к тексту, удалить и отправить заново.
- Медиа -> медиа другого типа (photo, video, animation, live photo): документация запретов не называет, кроме альбомов: внутри альбома можно только audio->audio, document->document, остальное на photo/live photo/video. Фактические переходы photo -> animation -> video/photo в группе надо проверить [не проверено, тест T6].
- Для inline-сообщений (`inline_message_id`) новый файл загрузить нельзя, только `file_id` или URL. Для rich в inline-сообщении тоже нельзя загружать новые файлы и указывать явные URL [док].
- Через локальный сервер можно передавать файл путём (`file://` или локальный путь), то есть без multipart по сети. В `tg` это `InputFileLocal`.
- Повтор тех же байтов даёт "message is not modified"; `tg.EditMessageMedia` уже трактует это как успех.

### 3.2 Анимация, MP4, live photo

- `sendAnimation`: "GIF or H.264/MPEG-4 AVC video without sound", до 50 МБ на облаке [док]. MP4 со звуковой дорожкой будет видео, не анимацией. `InputMediaAnimation` есть для `editMessageMedia` и `sendMediaGroup` не принимает анимации (только audio/document/live photo/photo/video).
- Наблюдение из соседнего проекта: `maply` шлёт MP4 без звука через `SendDocument`, потому что в `tg` нет `sendAnimation`; Telegram сам превращает такой файл в анимацию, а `file_id` потом используется в настоящем `sendAnimation` (`maply/internal/telegram/telegram.go`). Для `editMessageMedia` нужен именно `InputMediaAnimation`.
- Прогрессная гифка: заготовить несколько петель заранее (по языкам/темам), один раз отправить в служебный чат, сохранить `file_id` (на каждого бота свой, `file_id` между ботами не переносится [док]). Тогда превращение сообщения в "идёт генерация" это один `editMessageMedia` по `file_id` без загрузки. Для персональных петель с миниатюрами фото пользователя есть Go-рендерер `maply/internal/anim`.
- `sendLivePhoto` / `InputMediaLivePhoto` (10.0): фото плюс видео до 10 секунд и до 10 МБ; по URL нельзя, только `file_id` или загрузка; допускается в альбомах и в `editMessageMedia`. Подходит как формат для "картинка с лёгким движением", не как замена видео.
- `sendVideo`: MP4 H.264, нужны `width`, `height`, `duration`, `supports_streaming`; есть `cover` и `start_timestamp` (8.3). Входящий `Video` может иметь `qualities` (9.4).
- Видео, отправленные, скопированные или пересланные в группы и каналы с большой аудиторией, сервер может поставить в отложенную отправку до перекодирования; тогда `message_id` в ответе равен 0, и сообщением нельзя пользоваться до фактической отправки [док: предупреждения в 7.11 и 8.0]. Для видео-бота в больших группах: проверять `message_id == 0` в ответе и не пытаться сразу редактировать или удалять.

### 3.3 Лимиты размеров и частоты

| Что | Облако api.telegram.org | Наш `--local` сервер |
| --- | --- | --- |
| Скачивание через `getFile` | до 20 МБ [док] | без ограничения; `file_path` это абсолютный путь на диске сервера [док] |
| Загрузка multipart | фото до 10 МБ, остальное до 50 МБ [док: Sending files] | до 2000 МБ; можно путём к локальному файлу [док: Using a Local Bot API Server] |
| Отправка по URL | фото до 5 МБ, остальное до 20 МБ | то же [не проверено] |
| Отправка по `file_id` | без ограничений [док] | то же |
| Фото | суммарно ширина+высота не больше 10000, соотношение сторон не больше 20, до 10 МБ (написано в параметре `photo` метода `sendPhoto`) | ограничение на фото в документации не снято, считать теми же 10 МБ [не проверено] |
| Live photo | видео до 10 с и 10 МБ | то же |
| Подпись к медиа | 1024 символа после разбора entities | то же |
| Текст сообщения | 4096 | то же |
| Rich message | 32768 символов, 500 блоков, 16 уровней вложенности, 50 медиа, 20 колонок таблицы | то же |
| `callback_data` | 1-64 байта | то же |
| `copy_text` | 1-256 символов | то же |
| Inline-ответ | до 50 результатов | то же |
| Частота | в одном чате не больше 1 сообщения в секунду (короткие всплески допускаются), в группе не больше 20 сообщений в минуту, массовая рассылка около 30 в секунду [док: faq] | то же |

Вывод по результатам: большие PNG отдавать документом (`sendDocument`) как "оригинал", а фото в чате делать сжатым JPEG до 10 МБ. На частоту 20 сообщений в минуту в группе приходятся новые сообщения; считаются ли в неё правки, документация не говорит, поэтому правки прогресса делать не чаще раза в 3-5 секунд на сообщение [не проверено].

### 3.4 Rich messages (10.1, 10.3)

- Что это: сообщения со структурой: заголовки, списки, таблицы, цитаты, раскрывающиеся блоки, формулы, карты, `tg-collage`, `tg-slideshow`, блоки photo/video/animation/audio/voice_note/document, кнопки прямо внутри сообщения (`<tg-button>`, `RichMessageButton`, 10.3). Разметка: Rich Markdown (GFM плюс HTML), Rich HTML или явные `blocks`. Демо: https://t.me/RichTextDemoBot
- Методы: `sendRichMessage` (chat_id любой, `message_effect_id` только для личных), `editMessageText` с `rich_message`, `sendRichMessageDraft` (только личные чаты, 30-секундный черновик), `editEphemeralMessageText` с `rich_message`. Медиа в Markdown/HTML задаются ссылками `tg://photo?id=`, `tg://video?id=`, `tg://document?id=`, `tg://audio?id=` и массивом `media` (10.2).
- Переходы: rich -> медиа через `editMessageMedia` можно, медиа -> rich нельзя [док]. Для превью "вот такие фото взяты" это значит: если начать с rich-сообщения с `tg-collage`, дальше можно перейти в анимацию и в результат; обратного пути нет.
- Ограничения: `web_app` кнопка только в личных чатах; `login_url` в `RichMessageButton` без `bot_username`; `tg-thinking` / `InputRichBlockThinking` только в черновиках.
- Что не знаю: как rich-сообщение выглядит в клиентах, которые вышли до июня 2026, и работает ли оно во всех типах групп. Для группового превью выбрал бы классическое фото с inline-клавиатурой, а rich оставил для личного чата (отчёты, таблицы сравнения моделей) [не проверено, тест T7].

### 3.5 Ephemeral messages (10.2, 10.3)

- Сообщение в группе, видимое только одному пользователю и боту. Может пропасть само или при перезапуске клиента; доставка не гарантирована [док: bots/api#ephemeral-messages-and-commands].
- Отправка: любые `sendMessage`, `sendPhoto`, `sendVideo`, `sendAnimation`, `sendDocument`, `sendAudio`, `sendVoice`, `sendSticker`, `sendVideoNote`, `sendLivePhoto`, `sendContact`, `sendLocation`, `sendVenue` и `sendRichMessage` с параметром `ephemeral_message_parameters` {`receiver_user_id`, `callback_query_id`, `replace_callback_query_message`}. До 10.3 параметры были плоскими, и `tg` уже учитывает новый формат.
- Условие: обычный бот может отправить ephemeral только в течение 15 секунд после допустимого события: `callback_query_id` нажатой кнопки или ответ на входящее ephemeral-сообщение (`reply_parameters.ephemeral_message_id`). Бот-администратор группы может писать ephemeral любому не-боту в любое время [док]. Обычная команда `/f` не считается допустимым событием.
- Ephemeral commands: у `BotCommand` есть `is_ephemeral`. Такая команда не видна ни людям, ни другим ботам в чате; бот получает её, а отвечать на неё можно ephemeral-сообщением (ответ на ephemeral должен быть ephemeral) [док].
- `replace_callback_query_message` показывает ephemeral поверх исходного сообщения для нажавшего, остальные видят оригинал.
- Правка и удаление: `editEphemeralMessageText`, `editEphemeralMessageMedia` (с 10.3 с загрузкой файлов), `editEphemeralMessageCaption`, `editEphemeralMessageReplyMarkup`, `deleteEphemeralMessage`. `ephemeral_message_id` может быть переиспользован после удаления или истечения.
- `login_url` в ephemeral не поддерживается [док].
- Что это даёт framy: панель выбора модели и настроек, превью "вот что взято" и кнопки "Да/Нет" можно показывать приватно, не засоряя группу; публичным остаётся только результат. Условие: либо бот админ (тогда ephemeral возможен после любой обычной команды), либо `/f` объявлена как ephemeral command. Неизвестно, приходит ли `reply_to_message` у ephemeral-команды с ответом на обычное фото [не проверено, тест T5]. Для первой версии оставил бы публичное превью по вашему сценарию, а ephemeral подключил вторым этапом после теста.

### 3.6 Стриминг и черновики

- `sendMessageDraft` (9.3, для всех ботов с 9.5) и `sendRichMessageDraft` (10.1): `chat_id` только личный чат. Черновик живёт 30 секунд, потом нужно отправить итог обычным `sendMessage` / `sendRichMessage`. Одинаковый `draft_id` анимирует изменения. Пустой текст показывает "Thinking...". В rich-черновике можно `<tg-thinking>`.
- 10.3: `can_stop` показывает пользователю кнопку остановки, нажатие приходит как update `stopped_message_generation` ({chat, message_thread_id, draft_id}); `keep_on_stop` оставляет черновик на экране после остановки.
- В нашем исходнике сервера проверки типа чата нет [код], ограничение накладывает Telegram. В группах не рассчитывать.
- Для генерации картинок черновик годится только в личке: текстовые стадии, плюс медиа по `file_id` в rich-черновике. Загрузка новых файлов и URL в черновике не поддерживаются [док].

### 3.7 Кнопки

| Возможность | Версия | Где работает |
| --- | --- | --- |
| `style`: `primary` (синий), `success` (зелёный), `danger` (красный) | 9.4 | везде; в старых клиентах (до 09.02.2026) выглядит обычной кнопкой |
| `icon_custom_emoji_id` | 9.4 | только если у владельца бота Premium (в сообщениях от бота в личных, группах и супергруппах) или бот владеет доп. username на Fragment |
| `copy_text` | 7.11 | везде, до 256 символов |
| `disabled` (`DisabledButton`) | 10.3 | неактивная кнопка; не комбинируется с `callback_data`/`url` |
| `force_reply` в `InlineKeyboardMarkup` | 10.3 | показывает интерфейс ответа; при privacy mode ответ бот получит |
| `web_app` | 6.0 | только личные чаты. В группе открывать Mini App URL-кнопкой на `https://t.me/<bot>/<app>?startapp=...` |
| `login_url` | 5.x | вход на сайт одним нажатием (нужен `/setdomain` в BotFather); не в ephemeral |
| `switch_inline_query`, `..._current_chat`, `..._chosen_chat` | 6.7 | не для business и direct messages |
| `url` с `tg://` | давно | `tg://user?id=` упоминание |

Меню выбора (модель, соотношение сторон, количество, качество) строить правкой клавиатуры `editMessageReplyMarkup` на нажатие, без новых сообщений [рекомендация из features#inline-keyboards].

### 3.8 Остальные украшения

- Реакции: `setMessageReaction`, бот ставит одну реакцию; если сообщение из альбома, реакция уйдёт на первое неудалённое в группе [док]. Список допустимых эмодзи в документации (в моей выгрузке эмодзи потерялись, смотреть https://core.telegram.org/bots/api#reactiontypeemoji). Использовать как мгновенное подтверждение на сообщение с `/f`. Чтобы убрать: `deleteMessageReaction` (10.0). Апдейты `message_reaction` боту приходят только если он админ и указал их в `allowed_updates` [док].
- `sendChatAction`: `upload_photo`, `record_video`, `upload_video`, `upload_document`; держится до 5 секунд, работает в группах (не в каналах), `message_thread_id` поддерживается.
- Message effects (7.4): только личные чаты. Список id в Bot API нет (он в MTProto `messages.getAvailableEffects`). Известные id из сообщества: 5104841245755180586 (огонь), 5107584321108051014 (лайк), 5046509860389126442 (конфетти), 5044134455711629726 (сердце) [память, не проверено].
- `has_spoiler` на фото, видео, анимации (для чувствительных результатов).
- `show_caption_above_media`, `expandable_blockquote` в подписи (`<blockquote expandable>` в HTML): длинный промпт в подписи прятать в раскрывающуюся цитату.
- Entity `date_time` (9.5): время показывается в часовом поясе читателя; для "будет готово около ...".
- `protect_content`, `link_preview_options`, `message_thread_id` для топиков.

### 3.9 Как показать прогресс генерации (по возрастанию затрат)

1. Реакция на команду плюс `sendChatAction` каждые 4 секунды: ноль сообщений, укладывается в лимиты.
2. Превращение сообщения в анимацию "идёт генерация" через `editMessageMedia` (одна правка, `file_id` заготовлен).
3. Правка подписи (`editMessageCaption`) со стадией и ETA, не чаще раза в 5-10 секунд.
4. В личке: `sendRichMessageDraft` со стадиями и кнопкой Stop (`can_stop`).
5. Результат: короткая работа (до примерно 25 секунд) правкой того же сообщения; долгая отправкой нового сообщения ответом на команду пользователя (правка не даёт пользователю уведомления, ответ даёт) с удалением сообщения-прогресса через `deleteMessage`.

## 4. Сценарий C: Mini App

### 4.1 Возможности по версиям [док: bots/webapps]

| Возможность | Версия | Для framy |
| --- | --- | --- |
| `initData` + HMAC-проверка (`hash`) и Ed25519-подпись (`signature`) для третьих лиц | 8.0 | вход без паролей; сайт-сервис может проверять без токена бота (ключи в документации) |
| `requestFullscreen`/`exitFullscreen`, `lockOrientation`, `safeAreaInset`, `contentSafeAreaInset`, события `fullscreenChanged`, `safeAreaChanged` | 8.0 | галерея и просмотр результата; уважать safe area |
| `isActive` и события `activated`/`deactivated` | 8.0 | пауза опроса, пока свёрнуто |
| `addToHomeScreen`, `checkHomeScreenStatus` | 8.0 | ярлык на домашний экран |
| `shareMessage(msg_id)` + `savePreparedInlineMessage` | 8.0 | отправка результата в выбранный чат от имени пользователя |
| `downloadFile({url, file_name})` | 8.0 | нативное скачивание оригинала; нужны заголовки `Content-Disposition: attachment` и `Access-Control-Allow-Origin: https://web.telegram.org` |
| `shareToStory(media_url, {text, widget_link})` | 7.8 | публикация в сторис; ссылка в сторис только у Premium |
| `CloudStorage` | 6.9 | до 1024 ключей на пользователя, ключ 1-128 символов, значение до 4096; синхронизируется между устройствами |
| `DeviceStorage` | 9.0 | до 5 МБ на пользователя и бота, локально |
| `SecureStorage` | 9.0 | Keychain (iOS) / Keystore (Android), до 10 элементов на пользователя, есть `restoreItem` |
| `HapticFeedback`, `BottomButton` (MainButton/SecondaryButton, `iconCustomEmojiId` с 9.5), `BackButton`, `SettingsButton`, `showPopup`, `showConfirm` | 6.x-9.5 | нативный вид; MainButton как "Сгенерировать" |
| `requestEmojiStatusAccess`, `setEmojiStatus` | 8.0 | для генератора бесполезно |
| `LocationManager`, `Accelerometer`, `DeviceOrientation`, `Gyroscope` | 8.0 | эффект параллакса при просмотре, не приоритет |
| `BiometricManager` | 7.2 | защита BYO-ключа, если решите его хранить |
| `requestChat` + `savePreparedKeyboardButton` | 9.6 | выбор чата для отправки |
| `Serverless.call` | в списке полей без версии | размещение на Telegram Serverless, документации не нашёл, нам не нужно |

События и параметры, которые пригодятся: `viewportChanged`, `themeChanged`, `invoiceClosed`, `popupClosed`. Подключение: `<script src="https://telegram.org/js/telegram-web-app.js?64">` в `<head>` перед остальными скриптами. `isVersionAtLeast(version)` для проверки клиента.

Защита по origin (10.2, включена с 20.07.2026): методы `Telegram.WebApp` не работают из страниц с другим origin, чем у зарегистрированного Mini App. Отключается в BotFather Mini App на ваш риск. Для framy это значит: Mini App должен жить на одном origin; редиректы на другие домены (платёжные страницы, OIDC на oauth.telegram.org, CDN в iframe) потеряют доступ к Telegram API.

### 4.2 Запуск из группы и передача контекста [док]

Семь способов запуска: кнопка Main Mini App в профиле бота, keyboard button, inline button, menu button, inline mode, direct link, attachment menu.

- Inline-кнопка `web_app` только в личных чатах. В группе использовать URL-кнопку на direct link: `https://t.me/<bot>/<app>?startapp=<payload>[&mode=compact]` или для главного приложения `https://t.me/<bot>?startapp=<payload>`.
- Mini App из direct link получает в `initData`: `chat_type` (`sender`, `private`, `group`, `supergroup`, `channel`), `chat_instance` (глобальный идентификатор чата), `start_param` (он же GET `tgWebAppStartParam`), `user`. Идентификатора самого чата и `query_id` нет; в чат нельзя ни читать, ни писать от имени пользователя. Чтобы отправить сообщение, пользователь возвращается в inline-режим (`WebApp.switchInlineQuery`) или использует `shareMessage`.
- Attachment menu (контекст чата и `query_id`, `answerWebAppQuery`) доступна только крупным рекламодателям Telegram Ads, на тестовом сервере всем [док]. Рассчитывать нельзя.
- Как передать id группы и задачи: подписанный `startapp`. В соседнем `maply` это уже сделано: payload 36 символов (версия, дата, chat_id, 16 байт HMAC) в `maply/internal/auth/payload.go`, проверка `initData` в `initdata.go`, проверка членства `getChatMember` в `member.go`. Для framy payload подписывать (ключ производный от токена бота), класть туда id задачи и истечение, а членство или совпадение `initData.user.id` с автором задачи проверять на сервере.
- Связать `chat_instance` с `chat_id` через `callback_query.chat_instance` можно, если значения совпадают; в документации оба описаны как "global identifier of the chat", но равенство явно не заявлено [не проверено, тест T8].

### 4.3 Хранение ключа API пользователя

Можно ли хранить в `SecureStorage`: технически да (шифруется в Keychain/Keystore, до 10 элементов, привязано к устройству, доступно только этому боту, есть `restoreItem` с разрешением пользователя). Стоит ли: не как источник истины.

- Генерация из группы идёт на нашем сервере. Ключ, лежащий только на устройстве, ему недоступен, поэтому сценарий A с пользовательским ключом не работает.
- Ключ живёт на одном устройстве; на других его не будет (синхронизации нет, только `restoreItem` на том же устройстве).
- Доступность на десктопе и веб-клиенте в документации не указана [не проверено, тест T12].
- Если нужен BYO-ключ: хранить на сервере зашифрованным (envelope encryption), принимать только по HTTPS из Mini App или сайта, никогда не логировать. `SecureStorage` допустим лишь как режим "ключ не покидает устройство" с генерацией напрямую из Mini App (зависит от CORS провайдера) и без работы в боте.
- Мелкие предпочтения интерфейса (последняя модель, черновик промпта) класть в `CloudStorage` (синхронизируется) или в профиль на сервере.

### 4.4 Загрузка файлов из Mini App

Лимитов Telegram на загрузку из Mini App в документации нет: это обычная веб-страница с `<input type="file">`, ограничивает наш сервер и прокси. Если сайт стоит за Cloudflare Tunnel (как `ai.ws04.cc` в заметках), действует лимит тела запроса Cloudflare плана (по памяти 100 МБ для free и pro) [память, не проверено]. Для больших видео на вход: загрузка кусками или прямая загрузка в хранилище, а не одним POST.

### 4.5 Отправка результата из Mini App в чат

1. На сервере: результат (фото/видео) отправить ботом в служебный чат, получить `file_id` (для inline-результата по `file_id` он должен быть известен именно этому боту).
2. `savePreparedInlineMessage(user_id, result=InlineQueryResultCachedPhoto|CachedVideo|CachedMpeg4Gif, allow_user_chats/allow_group_chats/...)` -> `PreparedInlineMessage {id, expiration_date}`; id устаревает.
3. В Mini App: `Telegram.WebApp.shareMessage(id, cb)` -> нативный выбор чата; событие `shareMessageSent`/`shareMessageFailed`. Сообщение уходит от пользователя через бота (пометка "via @framybot").
4. Скачать оригинал: `WebApp.downloadFile`.

## 5. Сценарий D: сайт как главный интерфейс, вход через Telegram

### 5.1 Что есть

Найден новый вход на базе OpenID Connect: https://core.telegram.org/bots/telegram-login. Старый iframe-виджет в документации помечен как архивный [док].

- Discovery: https://oauth.telegram.org/.well-known/openid-configuration (проверен живьём 26.10.2026). Endpoints: `https://oauth.telegram.org/auth`, `/token`, `/.well-known/jwks.json`. Authorization Code Flow, `response_types_supported: code`, PKCE `S256` и `plain`, `token_endpoint_auth_methods: client_secret_basic, client_secret_post`, `subject_types: public`, `id_token_signing_alg`: RS256 (по умолчанию), ES256, EdDSA, ES256K. Scopes в discovery: `openid`, `phone`, `profile`, `telegram:bot_access`, `ton:wallet`.
- Настройка: бот в BotFather Mini App -> Login Widget -> Allowed URLs (origin сайта и redirect_uri) -> выдаются Client ID и Client Secret. Telegram редиректит только на заранее зарегистрированные URL.
- Scopes и claims [док]: `openid` даёт `sub, iss, iat, exp`; `profile` даёт `id, name, preferred_username, picture`; `phone` даёт `phone_number` (с согласия); `telegram:bot_access` разрешает боту писать пользователю в личку после входа. Отдельного UserInfo endpoint нет (в клиентских OIDC-библиотеках отключить запрос userinfo).
- JS-библиотека: `Telegram.Login.init/open/auth` с `client_id`, `scope` (`profile`, `phone`, `write`), `lang`, `nonce`; результат содержит `id_token`. Серверная проверка `id_token` обязательна (подпись по JWKS, `iss`, `aud` = Client ID, `exp`). Если на сайте заголовок `Cross-Origin-Opener-Policy: same-origin`, popup-вход ломается; нужен `same-origin-allow-popups` или без заголовка.
- Нативные SDK для iOS и Android.
- Для Go: библиотека не требуется, проверка JWT RS256 по JWKS укладывается в стандартную библиотеку; либо `coreos/go-oidc` и `golang.org/x/oauth2`.

### 5.2 Привязка аккаунтов сайта и бота

Единый ключ пользователя: числовой Telegram user id. Он один и тот же в `message.from.id` бота, в `initData.user.id` Mini App и в claim `id` из OIDC (нужен scope `profile`). Claim `sub` в примере документации не равен `id`; как ключ не использовать [не проверено, зачем отдельный `sub`].

- Вход через Telegram на сайте и в Mini App автоматически даёт один и тот же аккаунт, привязка не нужна. Пользователь, пришедший из группы, получает аккаунт при первом `/f`.
- Если у человека уже есть аккаунт на сайте (почта): одноразовая привязка через deep link `https://t.me/framybot?start=link_<nonce>` (параметр до 64 символов), бот получает `/start link_<nonce>` и записывает `from.id` к аккаунту. Обратное направление: inline-кнопка `login_url` из бота (автовход на сайт, нужен `/setdomain`).
- Scope `telegram:bot_access` (в JS-библиотеке `write`) при входе даёт боту право писать в личку без предварительного `/start`; использовать для доставки результатов, сгенерированных на сайте. Для этого логин должен идти от того же бота, что и генерация.
- Проверка `initData` в Mini App: HMAC с ключом `HMAC_SHA256("WebAppData", bot_token)` (в нашем `maply/internal/auth/initdata.go` уже реализована, окно свежести `auth_date` 24 часа). Если сайт отдельным процессом без токена бота: проверка Ed25519-подписью (`signature`) с публичными ключами из документации.

## 6. Сценарий E: inline-режим и guest mode

### 6.1 Inline: прошлые генерации

- Результаты по `file_id`: `InlineQueryResultCachedPhoto`, `CachedVideo`, `CachedMpeg4Gif`, `CachedGif`, `CachedDocument` [док]. `file_id` обязан быть известен этому боту, поэтому каждый готовый результат один раз отправлять и сохранять `file_id` и `file_unique_id` в таблице генераций. Без повторной загрузки, без лимитов размера.
- До 50 результатов в ответе, пагинация `next_offset` (до 64 байт). `is_personal: true` для личной истории, `cache_time` короткий (по умолчанию 300 секунд, для личной истории много).
- `InlineQuery.chat_type`: `sender`, `private`, `group`, `supergroup`, `channel`; позволяет показывать разные наборы.
- Кнопка над результатами `InlineQueryResultsButton`: `web_app` (Mini App, оттуда `WebApp.switchInlineQuery` возвращает в inline-режим) или `start_parameter` (переход в личку бота, например для входа). Параметр `button` в `answerInlineQuery` сейчас в `tg.InlineAnswer` отсутствует.
- Результату можно приложить `reply_markup` (кнопка "Открыть в framy" как `url`, или callback). Если клавиатура есть и включена обратная связь, в `chosen_inline_result` приходит `inline_message_id`, по нему сообщение можно править. Новый файл в inline-сообщение загрузить нельзя, только `file_id` или URL.
- Генерация из inline: ответить `InlineQueryResultArticle` с `InputTextMessageContent` "генерация..." и клавиатурой, потом по `inline_message_id` заменить на медиа (`editMessageMedia` по `file_id`; для inline-сообщений `deleteMessage` недоступен). Окно ответа на inline-запрос порядка нескольких секунд, поэтому генерация сама в окно не помещается [док в `tg/inline.go`].
- Включить `/setinline` и `/setinlinefeedback` в BotFather.

### 6.2 Guest mode (10.0) как второй вход в чаты без бота

- Включается в BotFather Mini App ("Guest Mode"); бот получает `supports_guest_queries` в `getMe`.
- Пользователь упоминает `@framybot` (до 3 гостевых ботов в сообщении) в чате, где бота нет, или отвечает на его сообщение. Бот получает update `guest_message` с сообщением-триггером и, если есть, сообщением, на которое оно отвечало [док]. Истории чата и списка участников бот не получает.
- Ответ: один раз `answerGuestQuery(guest_query_id, result=InlineQueryResult)`; возвращает `SentGuestMessage {inline_message_id}`. В `result` допустим `InputRichMessageContent` (10.1).
- Идеально подходит под "ответить на картинку и написать @framybot сделай в стиле аниме" без добавления бота в чат (в features прямо названо: image generation). Ограничения: правка только по `inline_message_id` через `file_id`/URL; сколько раз можно править и принимает ли ответ кнопки с callback, документация не уточняет [не проверено, тест T9]. Альбомы не решает (один отвечаемый контекст).

## 7. Сценарий F: Stars (кратко)

- Цифровые товары внутри Telegram только в Stars: валюта `XTR`, `provider_token` пустой [док: payments-stars]. Поток: `sendInvoice` или `createInvoiceLink` -> update `pre_checkout_query` (ответить `answerPreCheckoutQuery` за 10 секунд) -> сообщение `successful_payment` (проверять обязательно перед выдачей) -> хранить `telegram_payment_charge_id` для `refundStarPayment`.
- Из Mini App: `Telegram.WebApp.openInvoice(url, cb)` с ссылкой из `createInvoiceLink`; событие `invoiceClosed` (paid, cancelled, failed, pending).
- Подписки (8.0): `createInvoiceLink(subscription_period=2592000)` только 30 дней, цена до 10000 Stars; в `SuccessfulPayment` поля `subscription_expiration_date`, `is_recurring`, `is_first_recurring` (каждое продление приходит как новый `successful_payment`); с 10.2 update `subscription` (`BotSubscriptionUpdated`: `canceled`, `active`, `failed`); `editUserStarSubscription` отменяет или возвращает продление.
- Платное медиа `sendPaidMedia` (до 25000 Stars с 9.3): превью-замок для премиум-результатов.
- Обязательно для live-режима: ответ на `/terms` и `/paysupport` (поддержка по платежам), включить 2FA на аккаунте владельца [док].
- Риск политики: Telegram требует Stars для цифровых товаров внутри приложений Telegram независимо от внешних порталов. Единый баланс кредитов с пополнением на сайте картой и в Telegram Stars возможен, но пополнение картой нельзя предлагать из бота и Mini App [юридическая трактовка не проверена]. Таблица курса и комиссий на странице payments-stars не выгрузилась, смотреть вручную.

## 8. Остальное в 9.x-10.x, что реально может пригодиться

- Topics в личных чатах (9.3, 9.4): отдельные "проекты" внутри диалога с ботом (`message_thread_id` в send-методах, `createForumTopic`, включение в BotFather). Полезно для истории работ.
- `MessageGenerationStopped` и `can_stop` (10.3): остановка задачи из личного чата.
- Managed bots (9.6): пользователь может создать собственного брендированного бота под управлением framy (`getManagedBotToken`). Не сейчас.
- `live_photo` (10.0): входящий тип, учесть при разборе вложений.
- Bot-to-bot (10.0): боты видят сообщения друг друга при включении режима; для framy нет, но надо помнить о зацикливании, если в чате есть другие боты.
- `guard_bot` и Mini App для заявок в чат (10.1), communities (10.2), checklists (9.1, только business), gifts, business-аккаунты, истории (`postStory`): для генератора не нужны, пропускаю.
- Poll media, quiz: не нужны.

## 9. Что нужно добавить в `tg` (сверка с кодом)

Проверено по `tg/*.go` (последний тег v0.1.1-alpha.1). Есть: транспорт с long polling, `SendRichHTML`, `EditMessageRichHTML`, ephemeral (text, rich, edit, delete), `SendPhoto/Video/Audio/Document/MediaGroup`, `EditMessageMedia` (photo, video, document), `EditMessageReplyMarkup`, inline (photo, article), `Style` кнопок, `DisabledButton`, `Call` для остального, `Preflight`.

Нет или узко:

- Типы: в `Message` нет `media_group_id`, `animation`, `live_photo`, `rich_message`, `forward_origin`, `external_reply`, `quote`, `via_bot`, `has_media_spoiler`, `effect_id`, `successful_payment`. В `Update` нет `edited_message`, `pre_checkout_query`, `message_reaction`, `guest_message`, `stopped_message_generation`, `subscription`, `purchased_paid_media`. Часть данных достаётся из `Message.Raw`.
- `CallbackQuery.Message` значение, не указатель, и нет `inline_message_id` (в `docs/limitations.md` описано).
- Методы: `SendAnimation`, `InputMediaAnimation`, `SendLivePhoto`, `EditMessageCaption`, `ForwardMessage`, `CopyMessage`, `DeleteMessages`, `SetMessageReaction`, `AnswerGuestQuery`, `SavePreparedInlineMessage`, `SendMessageDraft`/`SendRichMessageDraft`, `EditEphemeralMessageMedia`, `CreateInvoiceLink`/`AnswerPreCheckoutQuery`/`RefundStarPayment`, `EditMessageText` по `inline_message_id`, получение `getMessages` (если будет патч, вызывать через `Call`).
- Кнопки: `web_app`, `login_url`, `copy_text`, `icon_custom_emoji_id`, `switch_inline_query`/`chosen_chat`; в `InlineAnswer` нет `button` и cached-результатов.
- `BotCommandScope` без chat id (нельзя заменить список команд для группы); `Probe` не расширяется вызывающим и не знает `sendAnimation`, `deleteMessages`, `answerGuestQuery` и т. п.
- Нет webhook (только long polling), что допустимо.

`maply` уже содержит локальные обёртки `SendAnimation` (через документ) и `web_app`-кнопки; перенести в `tg`, когда framy станет вторым потребителем (правило из `tg/docs`: метод переезжает в `methods.go`, когда он нужен второму боту).

## 10. Выводы для framy

### 10.1 Поток сообщений в группе по шагам

Предпосылки:

- BotFather: `/setcommands` с `f`; `setMyCommands` для `all_group_chats`; включить `/setinline`, `/setinlinefeedback`, Main Mini App, при желании Guest Mode; `/setdomain` для `login_url`.
- Бот админ группы (без лишних прав) либо privacy off; иначе альбомы деградируют по плану D из раздела 2.5.
- `allowed_updates`: `message`, `edited_message`, `callback_query`, `inline_query`, `chosen_inline_result`, `my_chat_member`, `pre_checkout_query`, `guest_message`, `stopped_message_generation`, `subscription`, при админстве `message_reaction`.
- `Preflight` требует `editMessageMedia`, `sendRichMessage` (если используется rich), `sendMessageDraft`, `deleteMessages`; список в `probeSafe` нужно расширить (см. раздел 9).

Постоянный фоновой шаг (если бот видит все сообщения): на каждое `message` с `photo`/`live_photo`/`animation`/`video`/`document(image/*)` и непустым `media_group_id` записать строку кэша (раздел 2.6).

Шаги команды `/f описание`:

1. Получить `update.message`, текст начинается с `/f` или `/f@framybot` (entity `bot_command`). Сразу `setMessageReaction(cmd, 👀)` и `sendChatAction(upload_photo, message_thread_id)` как подтверждение.
2. Определить режим:
   - нет `reply_to_message` и нет медиа в команде: генерация с нуля;
   - `reply_to_message` с `photo`/`live_photo`/`animation`/image-`document`: редактирование;
   - у reply есть `media_group_id`: источники = все элементы из кэша (после debounce 1,5 с); если в кэше пусто или один, попытаться B (`getMessages` по патчу) или C (forward-probe); если не получилось, пометить в превью "вижу 1 фото";
   - у reply есть `external_reply`: использовать его медиа как один источник.
3. Создать задачу в БД (состояние `awaiting_confirm`, chat_id, message_thread_id, user_id, id команды, список `file_unique_id`, промпт, модель, настройки, срок жизни 10 минут).
4. Скачать источники: `getFile` + чтение с диска (`tg.WithLocalFiles`), затем удалить файл (локальный сервер сам не чистит).
5. Отрисовать превью-коллаж (нумерация, число фото, обрезанный промпт, модель). Отправить `sendPhoto(chat_id, photo=InputFileLocal|Upload, caption=<HTML>, reply_parameters{message_id=cmd, allow_sending_without_reply:true}, message_thread_id, reply_markup)` с клавиатурой: [Да `success`] [Нет `danger`], [Модель], [Настройки], [Убрать фото], [Открыть в Mini App] (`url` на `t.me/framybot/app?startapp=<подписанный payload>`). `callback_data` = `j:<job>:go` и т. п. (до 64 байт). Сохранить `message_id` превью.
6. Нажатие кнопки: всегда `answerCallbackQuery`. Проверить `callback_query.from.id == job.user_id` (админам можно разрешить). Состояние задачи менять атомарно (CAS), чтобы двойное нажатие не запускало два раза.
   - Нет: `deleteMessage(chat, превью)`, задача `cancelled`. При админстве можно удалить и сообщение с `/f`.
   - Модель/Настройки/Убрать фото: `editMessageReplyMarkup` и при необходимости `editMessageCaption`, без новых сообщений.
   - Да: `editMessageMedia(chat, превью, InputMediaAnimation{file_id заготовленной петли}, reply_markup=[[Остановить]])`, подпись "идёт генерация, около N с" (правка подписи вместе с медиа в этом же вызове).
7. Пока идёт генерация: `sendChatAction(upload_photo | record_video)` каждые 4 секунды; `editMessageCaption` со стадией не чаще раза в 5-10 секунд; обработка `Остановить`.
8. Готово:
   - картинка и быстро: `editMessageMedia(chat, превью, InputMediaPhoto{файл с диска}, reply_markup=[[Ещё вариант][Доработать][Оригинал][Открыть в framy]])`; подпись: промпт в `<blockquote expandable>`, модель, seed;
   - видео или долго: `sendVideo(chat, video, width, height, duration, supports_streaming, cover, reply_parameters{message_id=cmd}, reply_markup)` (ответ на команду даёт уведомление), затем `deleteMessage(превью)`; если у ответа `message_id == 0`, не править и не удалять сразу;
   - большой файл: `sendDocument` как "Оригинал" по нажатию кнопки (а не автоматически);
   - сохранить `file_id` и `file_unique_id` результата для inline и Mini App, `(chat_id, message_id) -> generation_id` для ответов на сообщения бота;
   - `setMessageReaction(cmd, 🔥 или ✅)` вместо 👀.
9. Ошибка: `editMessageCaption` с причиной и кнопкой [Повторить] (медиа в текст не превратить, поэтому у сообщения остаётся картинка-заглушка).
10. Продолжение: пользователь отвечает на результат бота обычным текстом (без команды); приходит и при privacy mode; источник берётся из БД по `(chat_id, message_id)`; запуск шагов 3-8 с шагом подтверждения или без него (настройка чата).
11. Чистка: фоновое удаление просроченных превью (`deleteMessage`), TTL кэша 48 часов, чистка локальных файлов.

В личном чате то же без шага 2 про кэш (все элементы альбома приходят; собирать по `media_group_id` с debounce), а прогресс можно показывать `sendRichMessageDraft` с `can_stop`, результат отправлять `sendPhoto`/`sendVideo` с `message_effect_id`.

### 10.2 Решение проблемы альбома (итог)

- Основной путь: бот админ группы (или privacy off) + кэш `media_group_id` -> файл_ids (раздел 2.6). Подтверждён документацией.
- Запасной 1: патч `telegram-bot-api` с методом `getMessages` (раздел 2.5, вариант B): точный состав без прав, использует вызов TDLib, который сервер уже делает для `forwardMessages`. Рекомендую делать вторым этапом.
- Запасной 2: forward-probe в служебный чат с эвристикой группировки (вариант C), только до патча, и всегда с превью "что взято".
- Последний: альбом в личку, загрузка на сайте или в Mini App (вариант D).
- Не помогают: `external_reply`, `quote`, `copyMessages` (возвращают только id), guest mode, `getUserPersonalChatMessages`.

### 10.3 Что брать из новых возможностей в первую очередь

Этап 1 (нужно для сценария A, всё есть на 10.3):

1. Кэш `media_group_id` и разбор вложений (в `tg` добавить `media_group_id`, `animation`, `live_photo`, `external_reply`).
2. Цепочка `editMessageMedia` фото -> анимация -> результат; `sendAnimation` и `InputMediaAnimation` в `tg`; заготовленные петли с `file_id`.
3. Стили кнопок `success`/`danger`; правка клавиатуры для выбора модели и настроек.
4. Реакция плюс `sendChatAction` как подтверждение и индикатор.
5. Подписанный `startapp` и проверка `initData` (переиспользовать `maply/internal/auth`).
6. Хранить `file_id` результатов (нужно для inline и `shareMessage`).

Этап 2:

7. Mini App: Main Mini App, fullscreen и safe area, `shareMessage` с `savePreparedInlineMessage`, `downloadFile`, `CloudStorage` для настроек, `HapticFeedback`, `BottomButton`, `addToHomeScreen`.
8. Вход на сайте через OIDC (`scope=openid profile telegram:bot_access`), единый ключ по Telegram user id, `login_url` кнопка из бота.
9. Inline-режим с историей генераций (cached photo/video/mpeg4gif) и `button` с `web_app`.
10. Патч `getMessages` в сервере.
11. Stars: пополнение кредитов и подписка на 30 дней (`/terms`, `/paysupport`).

Этап 3 (после живых тестов):

12. Ephemeral-панель настроек и превью (если T5 покажет `reply_to_message` у ephemeral-команды).
13. Rich messages в личке: отчёты и таблицы сравнения моделей.
14. Guest mode для чатов без бота.
15. Личный стриминг прогресса: `sendRichMessageDraft` + `can_stop`; topics в личке.
16. Платное медиа, `shareToStory`.

Пропустить: business-аккаунты, gifts, checklists, communities, managed bots, emoji status, сенсоры и геолокация.

### 10.4 Что не проверено и как проверить (тестовый бот, наш сервер 10.3)

- T1. Privacy ON, не админ: ответ `/f` на элемент альбома. Ожидание: один `reply_to_message` с `media_group_id`, других элементов нет.
- T2. Админ без прав: приходят ли все сообщения альбома, можно ли выдать админство без единого права, что даёт ссылка `startgroup&admin=`.
  Проверено 31.10 на framy-test: да, админ без прав (`channels.editAdmin` только с флагом `other`) при включённом privacy получил все 3 фото альбома без подписи. Ссылку `startgroup&admin=` не пробовали.
- T3. `forwardMessage` элемента альбома в служебный чат: есть ли `media_group_id` в ответе, совпадает ли `forward_origin.date` у соседей, как ведёт себя лимит.
- T4. Патч `getMessages`: разрешает ли Telegram bot-аккаунту получать сообщения, которые он не видел (privacy ON).
- T5. Ephemeral-команда в ответ на обычное фото: есть ли `reply_to_message`; окно 15 секунд; ephemeral-медиа и `editEphemeralMessageMedia`.
- T6. Переходы `editMessageMedia` в группе: photo -> animation -> video и photo, rich -> animation; MP4 по локальному пути; поведение при размере больше 10 МБ для photo.
  Частично 31.10: photo -> animation -> photo в группе прошёл 28.10 (два `/img`, результат в сообщении превью). -> video в группе ещё не было.
- T7. Вид rich-сообщения (collage, кнопки) в старых клиентах и в группах.
- T8. Равенство `chat_instance` из Mini App и из `callback_query`.
- T9. Guest mode: `reply_to_message` с фото, число правок ответа, callback-кнопки на `inline_message_id`.
  Проверено 31.10 на framy-test: фото из ответа берётся, нажатия приходят как callback, 8 правок одного ответа прошли, карточка по ссылке подтягивается через Cloudflare. Подробности в design.md, Guest mode.
- T10. Видео в большой группе: `message_id == 0` и отложенная отправка.
- T11. `answerCallbackQuery.url` со ссылкой `t.me/<bot>/<app>?startapp=`.
- T12. `SecureStorage` и `Fullscreen` на десктопе и в веб-клиенте.
- T13. Считаются ли правки сообщений в лимит 20 сообщений в минуту для группы.

## 11. Ссылки

- Changelog: https://core.telegram.org/bots/api-changelog
- API: https://core.telegram.org/bots/api (якоря: #getupdates, #editmessagemedia, #sendanimation, #sendlivephoto, #forwardmessage, #copymessage, #sendmessagedraft, #sendrichmessage, #rich-message-limits, #ephemeral-messages-and-commands, #ephemeralmessageparameters, #inlinekeyboardbutton, #answerinlinequery, #inlinequeryresultsbutton, #savepreparedinlinemessage, #answerguestquery, #messagegenerationstopped)
- Mini Apps: https://core.telegram.org/bots/webapps
- Возможности ботов (privacy mode, guest, ephemeral, streaming): https://core.telegram.org/bots/features
- FAQ (лимиты): https://core.telegram.org/bots/faq
- Вход через Telegram (OIDC): https://core.telegram.org/bots/telegram-login
- Stars: https://core.telegram.org/bots/payments-stars
- OIDC discovery: https://oauth.telegram.org/.well-known/openid-configuration
- Исходник сервера на нашем пине: https://github.com/tdlib/telegram-bot-api/blob/e3e9dd8e5b3d7ab8537cd5a10dc31d5ffa8f82d1/telegram-bot-api/Client.cpp
- Репозитории FreshLabDev: `tg` (README, docs/limitations.md, docs/local-server.md, rich.go, ephemeral.go, capability.go, media.go, inline.go, types.go), `telegram-bot-api` (README, CHANGELOG), `maply/internal/auth`, `maply/internal/telegram/telegram.go`, `maply/internal/anim`
