# Changelog

All notable framy changes are documented here.

The `## <tag>` section of this file *is* the GitHub Release body: the release
workflow copies it verbatim and refuses a tag that has no section. Write it
for whoever has to decide whether to upgrade.

See [`docs/versioning.md`](docs/versioning.md) for what the numbers mean and
[`docs/releases.md`](docs/releases.md) for how a release is published.

## Unreleased

Nothing is released yet. The repository holds the design, the research it
was written from, and the skeleton of the service.

### Added

- The Go service skeleton. Configuration comes from the environment, and any
  secret can instead come from a file named by `NAME_FILE`, as Docker secrets
  do. PostgreSQL is used in the `framy` schema, with migrations embedded in
  the binary. Long polling records every sender, and every group framy is in,
  in `core` through `core.touch`. `Preflight` runs before polling starts.
  `/healthz` reports the version, the database and how fresh polling is.
  SIGTERM stops polling at once, but the updates already taken get up to 15
  seconds to finish, so a `/img` that comes in as framy stops still gets its
  preview instead of a draft cancelled halfway. The HTTP server stops within
  20 seconds.
- `/start` and the family's panels, in the sixteen languages of the family
  and the shape every sibling bot uses (`core/docs/panels.md`): home, help,
  about and the language picker in a direct chat; in a group `/start` is
  ephemeral, so the panel reaches only the one who asked, with the group's
  language for its admins. The language is core's, shared with the other bots:
  a pick writes a manual observation and Follow Telegram withdraws it. The
  command menu and the profile descriptions are published per language at
  start, and a description is written only when it changed.
- The home panel of a direct chat says where the person stands: how many of
  the free images (5 in any 24 hours) and the free video (1 in any 7 days)
  are left, which providers they have connected, their language, and the
  currency prices are shown in. A provider is named with a word beside it
  only when something is wrong with the key (invalid, needs billing, needs
  verification, no credits); no part of a key is ever shown. The currency is
  the person's choice, else the language's (Ukrainian, Belarusian and Polish
  map to UAH, BYN and PLN), else USD, since the bot has no country to go by.
  Each part is read when the panel is drawn, all under a 3-second deadline;
  one that cannot be read is left out and `/start` still answers. A group's
  panel shows none of this. With an https `SITE_URL` the panel gets an Open
  framy button that opens the site as a Mini App, and at start the menu
  button of every direct chat is set to open it, labelled framy. Without
  `SITE_URL`, or with one that is not https (Telegram refuses it), neither is
  set and the menu button is left as it was. The default models are not
  shown yet, since nothing stores them. The text that described the free
  allowance in words is gone from the panel, replaced by the numbers.
- `/metrics` with framy's counters in the Prometheus text format, on its own
  listener (`METRICS_ADDR`, `:9090` by default), apart from the public one.
- The model catalog. Every hour framy reads OpenRouter's public catalogs: the
  image models with the price of every upstream route, the video models, and
  their expiration dates. OpenAI's and Gemini's models are written by hand in
  `catalog/openai.yaml` and `catalog/gemini.yaml`, and `catalog/overlay.yaml`
  joins the offers of one model, so Nano Banana 2.1 comes from Gemini and from
  OpenRouter. The overlay also gives display names, the featured and free
  flags, the effort setting, values not to offer such as OpenAI's `auto`,
  pass-through settings, reference counts, what a model needs from an
  attached video, continuation and the face policy. The video editors and the
  upscaler (Aleph 2, FLUX Video Edit, FLUX Video Upscale) are in the video
  list and run only on an attached video; FLUX Video Edit takes up to 15 s and
  50 MiB. Hidden: models within 14 days of leaving, Google's preview ids,
  wrappers, closed OpenAI models and the talking-head model, which needs a
  voice the composer does not take yet. A model gone
  upstream gets `disabled_at` and comes back when it returns. A failed sync
  changes nothing, and one that would disable more than half of OpenRouter's
  offers is refused. `CATALOG_SYNC_INTERVAL` (`1h`, or `off`) and
  `OPENROUTER_API_BASE` configure it.
- The price estimate. Before a run framy works out its price from the
  catalog's rates and the run's settings: OpenRouter's price lines and video
  SKUs, OpenAI's token formula, Gemini's tokens per size, Seedance's video
  tokens, per-reference charges and Aleph's minimum. Each estimate says
  whether it is exact, about, up to, from, or known only after the run, and
  keeps the rates and quantities it used. It agrees with OpenAI's published
  gpt-image-2 prices and OpenRouter's 16:9 example. The overlay's `price` hints fill in what the rates leave out, and
  every sync logs the price lines the estimate cannot read. MAI counts an
  input image by its size, about 1,100 tokens a megapixel as measured, so an
  edit's estimate includes its photos. Of two video SKUs that fit a run
  equally, one whose rate a broader SKU already has is left out, so Seedance
  with a clip and no sound is no longer dearer than with sound, and Kling
  with sound is exact. A model that takes clips as references bills every
  clip's seconds beside the output's, so the estimate counts them all,
  each rounded up to a hundredth of a second: Wan 3.0 and Hailuo 3 at the
  output's rate, HeyGen at its reference rate, Seedance as video tokens at
  the output's size. Two runs on the test bot showed it: Wan 3.0 with a
  15 s clip cost $0.85 against an estimate of $0.21, HeyGen $0.70 against
  $0.05; both estimates now come within a hundredth of a cent. GPT Image 2
  and 2.5 count an input image in 32 px patches, at most 1,536, as worked
  out on OpenAI's forum, so an edit's estimate includes its photos: a
  1280×853 photo took 1,080 tokens on the test bot, which the estimate had
  left out. Seedance is priced as it bills: at the size it makes (864×496
  at 480p 16:9 on Seedance 2.0, where OpenRouter lists 854×480), 24 frames
  a second and one more, and a reference clip as at least 3 s. Two paid runs
  of Seedance 2.0 Mini, 4 s of 480p with a 2 s and with a 3 s clip, cost
  $0.1485 each where the estimate said $0.121 and $0.141; it now matches
  both to the token. A size the overlay does not list, such as 9:21 or 4K,
  is "about".
- OpenRouter's discounts come off the price. The sync reads them from
  `/api/v1/models/{id}/endpoints`, the only place that carries them, and the
  estimate takes them off the total and keeps the listed price beside it, so
  the composer can show it struck through. On 28 October that is 15% off Wan
  3.0 and 50% off HeyGen Video 1, the free weekly video: framy showed $0.50
  for 5 s of Wan 3.0 at 720p, where OpenRouter charges $0.425. A discount
  that could not be read counts as none, so a bad hour only makes a price
  dearer.
- The job queue, on River in the `framy` schema. A job is a row in
  `framy.job` and a River job, created in one transaction, and it moves
  through draft, queued, running and done, failed, cancelled or expired. The
  rules about money: the mark that a paid request is out is written before the
  request goes, so a run cut short by a crash is never sent again; a
  provider's job id is saved the moment it comes back, so a retry asks
  instead of paying; only a 429 or a 5xx before the run started is retried,
  after the provider's wait. A repeated Telegram update starts nothing, one
  person runs at most two jobs at once and the rest wait in order, and a job
  keeps its estimate next to what the provider charged. A stop waits for runs
  in progress up to `JOBS_STOP_GRACE` (3m30s).
- The OpenRouter adapter for images, on `POST /api/v1/images`. A job sends
  its settings, its pass-through settings under the routes' own options and
  its reference images, and goes only to the routes that take them, the
  cheapest first. What no route takes is refused before the call, so it costs
  nothing. Such a run, in every adapter, fails as `invalid_request` with
  framy's own words, so the site does not show them as the provider's. `usage.cost` becomes the job's actual cost. A moderated prompt ends
  as `moderation` with its reasons, apart from money, key, refused, timeout
  and provider failures, and an error inside a 200 is read too. OpenAI's own
  filter, which OpenRouter passes on as a bare 400, is told apart by its
  words. Only a 429, a
  provider that is down or overloaded, and a connection never made are
  retried. framy's own key for the free allowance is `OPENROUTER_API_KEY`
  (or `OPENROUTER_API_KEY_FILE`).
- The OpenAI adapter for images, for a person's own key. A run is one call:
  `POST /v1/images/generations`, or `/v1/images/edits` when it has reference
  images, which go as the presigned R2 link OpenAI fetches. `quality` is
  always one of the model's levels, `low` to `max`, and never `auto`; `size`
  is a concrete WxH, any size up to 3840 px that OpenAI's rules allow. A run
  framy can tell is wrong is refused before the call, so it costs nothing.
  `safety_identifier` is a hash of the person's id, never the id. A
  `moderation_blocked` answer ends the run as `moderation` with its stage and
  categories, apart from money, key, refused, timeout and provider failures;
  only a 429 for the rate, a 503 and a connection never made are retried. The
  actual cost is the usage tokens times the catalog's rates for text, image
  input and image output.
- The Gemini adapter for images, for a person's own key. Nano Banana 2.1,
  3.1 Flash, 3.1 Flash Lite and 3 Pro run through the Interactions API, with
  `thinking_level` and Google Search grounding, which OpenRouter does not
  pass. References are fetched from their links and sent inline, so no link
  reaches Google, and the run is sent with `store: false`. A block or a
  `NO_IMAGE` answer is a refusal, not a failure, and the model's own words are
  kept for the person. A key from a project without billing (a 429 with
  `limit: 0`) fails at once as `key` with the status `needs_billing` and is
  never retried; a spent prepaid balance and a wrong key are told apart too.
  The actual cost is the usage tokens times the catalog's rates.
- Gemini Omni 1.1 Flash on a person's Gemini key, as a background
  interaction: video from words, from a start frame or from a start and an
  end frame (named in the prompt with `<FIRST_FRAME>` and `<LAST_FRAME>`),
  from up to 6 reference images, and from a source video of up to 10 s to
  edit or extend, every file sent inline. The interaction's id is saved
  before the first poll, so a retry never pays twice, and a poll every 30 s
  finishes the run. A video Google leaves as a file is downloaded with the
  key from the adapter's own base, never from the link in the answer. A
  background run needs `store: true`, so Google keeps it, for 55 days on a
  paid project; images still go with `store: false`. `previous_interaction_id`
  continues an earlier run when a run's settings carry it. A video may be at
  most 128 MiB, as on OpenRouter. A run that ends without a video says why
  and keeps what Google billed. Extending a clip is priced up to the
  source's length plus 10 s, since the whole result may be billed.
- More of Nano Banana on the Gemini key: Google Image Search grounding on
  2.1 and 3.1 Flash (`image_search`, beside web search or alone), PNG or
  JPEG output (`output_format`), the detail each input is read at
  (`media_resolution`, low to ultra high), and one video of up to 60 s as an
  input to 2.1, 3.1 Flash and Lite. A grounded run keeps what Google's terms
  ask to show with it, the Search Suggestions, with the queries and the
  cited pages (migration 016, `job.grounding`), and `GET /api/jobs/{id}`
  gives them as `grounding`. `/img` never turns search on, since a Telegram
  message cannot show them. Google's charge for search queries past its
  monthly allowance is in neither the estimate nor the cost. The actual cost
  is read by modality now, so thinking, tool use and a video input are each
  priced at their own rate.
- The free allowance: 5 images in any 24 hours and 1 video in any 7 days per
  Telegram user id, across every surface, counted from the job table. Free
  runs go through framy's own OpenRouter key with fixed settings: MAI 2.6
  Flash with one result, FLUX.3 Klein at 1K for albums of more than 5 photos,
  and the weekly video models. Any other model, setting or number of files is
  refused. A failed run does not count; a run refused for its content does.
  The check and the insert are one transaction under a lock per person and
  one for the budget, so two requests at once cannot both take the last slot.
  Two fuses close the tier for everyone: a budget of `FREE_DAILY_BUDGET_USD`
  ($1 by default) a day, until 00:00 UTC, and the limit on framy's OpenRouter
  key, read every minute from `GET /api/v1/key`. A free run that fails for
  lack of money pauses the tier for 15 minutes. The operator chat is told
  once a day per fuse. A refusal is a typed error that says when the slot
  frees up or until when the tier is closed, for the bot and the site to
  word. The bot says how long until the next free image, in hours and
  minutes.
- Provider keys that people paste in: OpenAI, Gemini and OpenRouter. A key is
  checked with the provider when it is saved (the list of models, which costs
  nothing) and kept only encrypted: a new data key for every record,
  AES-256-GCM with the person, the provider and the connection as associated
  data, and the data key wrapped by a key-encryption key read from a file
  (`FRAMY_KEK_FILE`), apart from the database and its backups. A new line in
  that file starts a rotation at the next start: only the data keys are
  rewrapped, the encrypted keys stay as they are. A connection shows its last
  four characters and a status: valid, verified, needs billing, needs
  verification, no credits or invalid. The list of models cannot tell the
  last three apart from a key that works, so those are set by what a run
  shows. A run on OpenRouter, OpenAI or Gemini pays from the person's own
  connection and never falls back to framy's key.
- A key pasted into a direct chat is deleted at once and the bot says that
  keys go in the settings, in all sixteen languages. A key pasted in by
  editing a message already sent is deleted the same way. If Telegram
  refuses the delete, the bot says the message stayed.
- Storage in Cloudflare R2. A photo a person brings goes through one ffmpeg
  subprocess: its format is read from its bytes (JPEG, PNG, WebP, HEIC,
  AVIF), its size from its header, so anything above 64 MP is refused before
  it is decoded, and ffmpeg reads only local files under a name framy picks.
  It joins the tiles of an iPhone HEIC, turns the picture by its orientation,
  drops EXIF and GPS, converts Display P3 to sRGB and brings the long side
  down to 2048 px, keeping PNG for a picture with alpha. Results are copied
  into the bucket the moment a run ends, so they outlive the provider's link,
  and a result fetched from a link goes only to public addresses. Inputs go
  to providers as presigned links that live 15 minutes; an input deleted
  before the run fails the job as `storage` before anything is paid, a job
  takes only its owner's files, and a provider's error that echoes a link
  is stored and logged without it. Deleting a file removes its object and
  its row. The host keeps working files only while their work lasts, and
  stray ones are swept every hour. The operator chat hears each time the
  bucket passes another 10 GB. `R2_ACCOUNT_ID` (or `R2_ENDPOINT`),
  `R2_BUCKET`, `R2_ACCESS_KEY_ID` and `R2_SECRET_ACCESS_KEY` (both also as
  `_FILE`), `WORK_DIR` and `OPERATOR_CHAT_ID` configure it; the image ships
  ffmpeg 8.1. `go test -tags r2live ./internal/r2` checks the real buckets
  and their token by hand.
- Prices in the person's currency: USD, UAH, EUR, PLN or BYN. Rates come
  from the NBU every day, and the next day's after 15:35 Kyiv, with
  `fawazahmed0/currency-api` behind it and for BYN, which the NBU does not
  list; when both fail the last saved rate stays. The sync asks only when the
  saved rates are behind, so a restart costs no request. Days missed while
  framy was down, weekends included, are read by date from both sources, up
  to 31 days back, so a price never falls back to an older day's rate. The
  default currency
  comes from the country on the site and from the language in the bot, and a
  person's choice is kept in `profile`. An amount is written the way the
  person's language writes it and never rounds down, with the rate's date
  beside it, and a job keeps the rate its price was shown with. `FX_SYNC=off`
  stops the sync.
- The pictures framy puts in a chat before and instead of a result, drawn on
  the CPU without a browser: the album preview, where the photos (one to ten),
  numbered 1 to N, float together over a soft shadow and never leave their
  places; the loop shown while a job runs, where a halftone of dots keeps
  turning from one figure into the next (a blob, a wave, a heart, mountains
  with a sun, a flower, a crescent); and a card for a job that failed, with
  the bot's line beside the cross in the person's language. All three are
  1280×720, 16:9, so a chat shows them as wide as a video. The loops are
  H.264 with no sound track, which Telegram plays as animations, and each
  ends on its own first frame, so they have no seam. A ten-photo album
  renders in under a second and is about 140 KB; the loop takes 0.6 s and
  530 KB. The loop and the cards are kept on disk under a key that changes
  with the design, so each is drawn and uploaded once. The card has no line
  in Chinese, Japanese and Arabic, whose letters the bundled fonts lack.
- The public listener's foundation, in `internal/web`: the site, the API, the
  event streams, the OAuth callbacks and the providers' webhooks all register
  on one server that gives them the same rules. Every HTML response carries
  one CSP that lets only Telegram Web frame the site and lets files in and out
  of the R2 endpoint; there is no X-Frame-Options, and nosniff, no-referrer,
  COOP same-origin and HSTS go on every response. A POST from another site is
  refused (`http.CrossOriginProtection`), except under `/hooks/`. `/api`,
  `/auth`, `/oauth`, `/events` and `/hooks` are never stored; only a hashed
  file under `/assets/` is cached for a year; a person's page is never
  `public`. Cloudflare Free allows one rate-limit rule, so the limits are in
  the service: a bucket per address and per signed-in person, stricter
  for sign-in, uploads and job creation, answering 429 with `Retry-After`.
  The visitor's address is read from `CF-Connecting-IP` only when the request
  comes from `TRUSTED_PROXIES`, the addresses cloudflared connects from. An
  event stream or a slow upload asks for longer deadlines per route.
- The site and the Mini App, first two stages, at `/`. A person signs in
  with Telegram on the site, or is signed in by the Mini App itself, and
  makes an image or a video from a prompt and photos: the kind, the model
  with its variants, the way to pay (the free allowance or a key), the
  settings the model has, and the price of exactly that run, which follows
  every choice. A model's `size` is a choice only where it is the one way to
  set the shape and the resolution, as on GPT Image; a video model that lists
  it beside its own ratio and resolution shows one row of each and runs
  without it. Fast mode is the site's own switch: on, the run goes to the
  model's fast mode (Wan 3.0 Prime for Wan 3.0). The model picker shows
  recommendations, the families by maker or by price with the cheapest way
  to run each, and a detail card. Every maker in the catalog has its mark;
  HeyGen's, Sourceful's and TBC's come from the makers' own sites.
  Create starts the run and the stage follows it live over the event stream
  (polling every 5 seconds when too many streams are open) until the result
  shows. On a phone the composer is a dock at the bottom and the settings
  open in a sheet; in Telegram, its main and back buttons stand in for ours.
  The page paints a skeleton at once and fetches the catalog and the person
  in its first line, the rest arrives as one script and one stylesheet of
  hashed names, and nothing comes from outside but Telegram's script. All
  the lines are in the sixteen languages. The editor of a result comes next.
- The site's stage, third stage: what happens when a run gives no result.
  A failed job says why in the person's words for each `error.class`,
  what came of the money (a run on the person's key that cost nothing shows
  its price as 0, one the provider may have charged says up to how much, a
  free one says the free run came back), and offers what helps: try again,
  edit the prompt, top up the key, or run a free run on the person's own
  key. The provider's own words show, small on the frame under its label,
  only for a refusal by its rules, a request it did not accept, or its
  failure, as its message without framy's prefixes or the JSON around it.
  A model that takes no real people (Seedance) or no children (Veo, Gemini
  Omni) says so in the picker's card and under the files before the run,
  and when the provider refuses a file for a person in it, the stage says
  "A person in the clip" or "in the photo" and offers another model. A
  refused sound (`sound`) says "Sound refused", costs nothing, and offers
  Try without sound, the same run with the sound off. A run
  framy did not send (`invalid_request`) and a setting the server refused
  before any job (`unknown_setting`) say "Not sent" and to reload the page.
  A job that
  waits for a retry counts down to `retry_at`, and one still in the queue can
  be cancelled; one already running says it cannot be stopped. A press the
  server refused before any job (a higher price, the free allowance used up
  or paused, files that do not fit, a clip too long) says so with the next
  step, such as "Create for" the new price. The page tells when it lost and
  regained its connection, and marks a run it reads every 5 seconds instead
  of the stream. A result has Again, Edit the prompt, Continue for a video
  (on the same model where it goes on, else from its last frame, which the
  composer marks with Continue's icon and prices with the frame in it) and a
  copy button for the prompt. A run that went on from a last frame shows,
  saves and sends the clip joined to it, everywhere the site shows a result,
  and saves its new part on its own with New part; Continue then takes the
  last frame of the whole clip. A video plays in the site's own player, the
  same in every browser and in the Mini App: play, a bar to seek on, the
  time, the sound and the whole screen, and on a joined clip the bar is cut
  into its parts with the one playing standing taller. In the Mini App it
  also has Share, which opens Telegram's chat picker with the result; one
  Telegram will not take says to download it instead.
- The stage shows every state of a run as a halftone of dots in the frame,
  as the bot's previews do: the bot's generating loop while the job runs,
  and a figure of its own for the start, the queue, a wait with what is left
  of it, photos going up, a clip filling up as it uploads, the film running
  while framy takes or cuts it, and each reason a run gave nothing (refused
  by the rules, no money, the key, too long, interrupted, no answer, files
  that do not fit, cancelled). The dots flow from one figure to the next,
  dim and stop while the page has lost framy, and stay still with reduced
  motion. A result shows through the dots as they swell until they touch,
  then settles in sharp. It is one canvas at 30 frames a second, and no task
  holds the main thread over 50 ms on the slowed CPU of the browser tests.
- Clips on the site. A clip is added like a photo, turns the composer to
  video and to a model that takes one, and shows its length; one longer than
  the model takes, or than a free run takes, opens an editor that picks the
  part, frame by frame, with a timeline of its frames. On Create the clip goes straight to the bucket by a
  presigned PUT with its progress shown, framy takes it or cuts the part, and
  a clip framy keeps already is only cut. A clip over a minute, a file that is
  not a video, or a format framy cannot read is refused with a line that says
  so; a clip only too long keeps its upload for a part.
- Settings on the site and in the Mini App, behind the account button at
  the top and, in the Mini App, Telegram's Settings button. OpenRouter
  connects through its own sign-in; in the Mini App that opens in the
  browser, and the sheet reads the account again when the person comes back.
  A key for OpenRouter, OpenAI or AI Studio (Google's, for Gemini; the site
  names the provider AI Studio, where the key is made) can be pasted
  instead: the field
  refuses what cannot be a key before anything is sent, framy checks the key
  with the provider, and the sheet says what the provider answered (a wrong
  or revoked key, a key that may not list the models, or no answer, with a
  10-second countdown before the next try). A connected key shows only its
  last 4 characters and what its last run showed (it works, the account is
  out of money, billing is off, the organization needs verifying, the
  provider no longer accepts it), with Check, Paste again and Disconnect;
  after a disconnect the sheet links to the provider's page to revoke the key
  there. The composer's step that needs a key, and a run refused for a
  missing or dead key, open that key's form. Under a key, what framy's runs
  on it cost by the provider's bill, in all and over 30 days. The sheet also
  shows the free runs left with when the next one comes; the models Create
  opens on, for pictures and for videos, chosen in the model picker, which
  goes back to the settings, or left to framy's choice; whether the site's
  results also come to the chat with framy; the currency prices show in (USD,
  UAH, EUR, PLN or BYN at the day's rate); framy's language, shared with the
  family's bots, Automatic until the person picks one in framy (the page
  reloads in it and keeps the prompt); how many files are stored; and
  sign-out. Deleting the account asks first and says that
  runs, files and keys go, on the site and in Telegram; runs still waiting
  can be cancelled from there, and a deletion that stopped halfway goes on
  with one more press.
- The library and the viewer, on the site and in the Mini App. Two tabs at
  the top switch between Create and the library, and on the site the
  address follows: `/library`, and `/r/<id>` for one run, which opens only
  for its owner. The library shows every run, newest first and by day, made
  on the site or in Telegram, with its cover, prompt, model and cost; a run
  still going shows its state and is read again every 5 seconds until it
  ends. It filters to images or videos, finds runs by a word of the prompt,
  and loads the next 30 as the end comes near. A run made by guest mode says
  so on its tile. The viewer shows the result (each one, for a run that made
  several), the settings, what it cost, the files it started from, and when
  and where it was made (the site, the Mini App, the chat with framy, a group
  or guest mode); the arrow keys go to the next and previous run. A result
  opens on the whole screen, where a swipe or the arrows go between the
  results of the run. Again runs it once more with the photos and clip framy
  still keeps, at today's price, which shows on the button before the press.
  Download, Edit the prompt, Continue and, in the Mini App, Share work as on
  the stage. Delete is there once a run has ended; it asks first, says that
  what the bot already sent to chats stays, and for a free run that the free
  run does not come back. When framy could not delete the files, the run
  stays and a second press carries on. In the Mini App, Telegram's back
  button closes the whole screen, then the viewer, then goes back to the
  composer.
- Sign-in, in `internal/auth`. On the site it is Telegram Login, Telegram's
  OpenID Connect with PKCE: framy checks the id token's signature, issuer,
  audience, expiry, nonce and state, and takes the account from the `id`
  claim, the same number a bot update carries. The session is framy's own,
  since Telegram gives no refresh token: a `__Host-` cookie that lasts 30
  days from sign-in, with only its hash in the database. A refused sign-in
  returns to `/?sign_in=cancelled`, `expired` or `failed`. In the Mini App
  `POST /auth/miniapp` checks `initData` (at most an hour old) and answers
  with a bearer token for an hour, because cookies in the Telegram Web frame
  are unreliable. It has a rate limit of its own, 120 a minute per address,
  since everyone who opens the Mini App calls it and a carrier puts many of
  them behind one address. Every sign-in keeps the name and photo Telegram
  gave (migration 012), since framy may not read names from `core`; the
  photo stays Telegram's link, and only links on `t.me` and Telegram's CDN
  are kept, the only Telegram hosts the CSP lets images come from. Sign out ends one
  session; sign out everywhere ends all of a person's sessions and tokens. The per-person rate limits now count the
  signed-in person. Without `TELEGRAM_LOGIN_CLIENT_ID` and
  `TELEGRAM_LOGIN_CLIENT_SECRET` the site's sign-in answers 503 and the rest
  runs.
- Connect OpenRouter, the way in for people without a key to paste:
  OpenRouter's OAuth with PKCE (S256). `GET /oauth/openrouter/start` sends a
  signed-in person to OpenRouter (anyone else signs in first and comes back);
  the callback checks a `__Host-` cookie against the state, so a flow can be
  finished only in the browser that started it, by the same person, within
  ten minutes, and once. The key OpenRouter issues is checked and encrypted
  like a pasted one. Every outcome returns to
  `/?connect=openrouter&result=connected`, `cancelled`, `expired`, `signin`,
  `refused` or `unavailable`; the code, the verifier and the key are in no
  URL, log line or error. It needs `SITE_URL` and the KEK.
- The job API the site and the Mini App will use, in `internal/api`, for
  the signed-in person. `GET /api/me` says who is signed in: the name and
  photo of the last sign-in, the language, and the currency prices are shown
  in. `POST /api/uploads` takes one photo through the storage pipeline. `POST /api/jobs` starts a job and answers 202 at once
  with its price in the person's currency; a required `Idempotency-Key`
  makes a repeated request return the same job. `GET /api/jobs/{id}` shows a
  job to its owner only, and `GET /api/jobs/{id}/events` follows it over
  SSE: the state now, then every change, a ping every 15 s, a resume from
  `Last-Event-ID`, at most three streams per person. No request waits for a
  generation, so Cloudflare's 125-second limit never cuts one. Changes come
  from triggers on the job row (`pg_notify`), so every path that changes a
  job is announced and a rolled-back change is not.
- What the composer needs before Generate. `GET /api/catalog` lists every
  model and offer with its parameters and the free allowance's locked
  settings, for anyone. `POST /api/quote` prices up to 200 runs at once with
  the estimate `POST /api/jobs` keeps, files still in the browser included,
  and for a signed-in person says what would stop each run: no connection,
  the free limit and when it frees up, the tier closed. `GET /api/me` now
  lists the person's connections (last four characters, status) and what is
  left of the free allowance. `POST /api/jobs` takes the price the person
  saw and refuses with 409 `price_changed` when the run would now show a
  higher one. A free video from a start frame started on the site names the
  frame, as the chat does, instead of being refused by the allowance, and a
  setting may be a short list such as `frame_images`.
- What the site's History and Settings need. `GET /api/jobs` is the
  history, newest first, by kind and by a part of the prompt, in pages of up
  to 100 with the links of every file signed in one read; each job now
  carries its model, prompt, settings, surface and inputs. `DELETE
  /api/jobs/{id}` deletes a job that ended with its results and the uploads
  no other job takes, and `POST /api/jobs/{id}/cancel` stops one that is
  still queued. `PUT`, `DELETE` and `POST .../check` on
  `/api/connections/{provider}` save, delete and re-check a key, and say
  plainly why a key was refused. `PATCH /api/me` sets the currency and the
  language or puts them back to the default, and `GET /api/me` tells how
  many files and bytes framy keeps for the person. `DELETE /api/me` deletes
  the account: every file, the keys, the prompts and results, the profile,
  and last every session; a failure half way leaves what is left to a
  second try. A deleted job keeps a cleared row (migration 017), so
  deleting a run, or the account, does not give free runs back.
- The model picker's shape comes from `catalog/overlay.yaml`, so a new model
  needs no code: families with their generations and tiers, the variant a
  family opens on, outdated variants worked out from newer generations,
  Wan 3.0 Prime as the fast switch of Wan 3.0 instead of a model of its own,
  suggestions for an image, an album, a video and a video edit, one name per
  maker, and display names for Kling and Hailuo. A variant's note is
  interface text in all sixteen languages. `GET /api/catalog` carries all of
  it.
- One rule picks the value of a setting nobody chose (`catalog.Defaults`):
  the shape of the attached photo, 1K or 720p, 5 seconds, medium quality.
  The catalog lists the defaults, a quote prices them, and a run on a key
  goes with them, so what is priced is what is sent. A setting some upstream
  of OpenRouter would not take keeps the provider's default, since sending
  it would narrow the upstreams that may run it.
- What a run may attach, per offer. `GET /api/catalog` gives every offer its
  `inputs`: how many images, and of them how many references and how many
  start and end frames; how many videos and sounds, each with its role (a
  reference, or the source an editor works on), the length of one clip and
  of all of them together, and the largest file; and how many files in all.
  A count is set in the overlay only where OpenRouter lists that kind of
  input for the model and the maker publishes the number: Seedance 2.x, Wan
  3.0, Hailuo 3, HeyGen Video 1, Grok Imagine Video 1.0, HappyHorse, and
  Seedream 5.0 capped at the 10 BytePlus takes. A quote or a job with more,
  or with frames and references together, which OpenRouter would drop, is
  refused with 422 `wrong_inputs` and the offer's `inputs`. A photo sent to
  a video model that takes no references goes as its start frame. A setting
  the offer does not have, such as `fast` on Wan 3.0, is refused with 422
  `unknown_setting` and its name, before a job exists.
- A model its maker deprecated says so: `deprecated`, `shuts_down` and
  `successor` in the catalog, from `deprecated` in the overlay. Its family
  opens on the successor and counts it outdated, and it is hidden 14 days
  before its shutdown day. A variant's `keep` stops a newer generation from
  outdating it while it still does what the newer one does not: Seedance
  2.0 makes 1080p and 4K, Grok Imagine Video 1.0 takes 7 reference images
  where 1.5 takes a start frame, and Qwen Image 2.1 Pro came out after Qwen
  Image 3. Marked on 28 October: Gemini 3.1 Flash Image, MAI-Image-2.5 and 2.5
  Pro, Grok Imagine Image Quality (2 November), Veo 3.1 and its Fast and
  Lite, Seedance 1.5 Pro (11 November). Seedance 2.0 Mini takes Veo 3.1
  Fast's place among the video suggestions.
- The catalog checked against every maker's documentation on 28 October.
  FLUX Video Upscale's `creativity` is 0 or 1, as BFL takes it, where the
  default was 0.5, which BFL refuses. HY Image's price is known: Tencent
  counts 15,000 tokens an image up to 2K and 20,000 at 4K. The first 2
  reference images of HeyGen Video and the first 5 of Hailuo H3 are free,
  as OpenRouter's pages say, so 5 s of HeyGen at 768p with two references
  is $0.50 before the discount, not $0.58 (`price.free_images` in the
  overlay). Riverflow 2 Fast
  offers no 4K, Kling Video O1 is hidden as it costs what Kling 3.0 Pro
  costs and does less through OpenRouter, Krea 2 opens on Medium, Aleph 2
  takes a source of 2 to 30 s, FLUX Video Edit of at least 0.7 s and FLUX
  Video Upscale of up to 20 s and 50 MiB, and Nano Banana 2 Lite on the
  Gemini key takes all 14 aspect ratios. `docs/research/makers.md` lists
  what each maker's own API offers beyond OpenRouter, for the direct ports
  to come.
- Riverflow 2.6 Pro, out on 29 October, opens the Riverflow family in place
  of 2.5 Pro, which is now outdated, at the same price. TBC's mischief 1, an
  image-to-video model out on 30 October, from $0.005 a second, runs only
  from a photo: `references.frame_required` in the overlay makes a run need a
  start frame, and the picker dims the model until a photo is attached.
- Video through OpenRouter (`POST /api/v1/videos`), with start and end
  frames and references sent as presigned links. The provider's job id is
  saved before the first poll, so a retry never pays twice; results are
  downloaded with the key from `/videos/{id}/content`, never from links in
  the answer. A poll every 30 s finishes every video. With `HOOK_PATH_SECRET`
  set and an https `SITE_URL`, a request also carries a webhook address with
  a path nobody can guess, and the webhook only brings the next poll
  forward: it never carries a result. On framy's own key its signature is
  checked when `OPENROUTER_WEBHOOK_SECRET` is set, and a repeated delivery
  does nothing twice. A finished video may be at most 128 MiB, since it is
  held in memory while it is copied to R2. The poll that downloads it gets
  5 minutes and the copy 3 more, where an image's call keeps its 3, so a
  large clip on a slow link is not cut off after it was paid. The free video
  plans (HeyGen, Grok Lite, FLUX Video Edit) run through it. Checked through
  the tunnel: a video finishes through the webhook on a person's OAuth key
  and, with a checked signature, on framy's own key; a forged signature or
  none is refused with 401.
- framy names itself to OpenRouter as `https://t.me/framyin_bot` (HTTP-Referer)
  and `framy` (X-Title) from every stack, so OpenRouter's activity and app
  pages point at the bot, not at this repository.
- The album cache that `/img` in reply to one photo of an album reads.
  Telegram sends every photo of an album as a message of its own, and a reply
  names only one, so framy keeps the photos, videos and image or video
  documents of the albums it sees (migration 013, table `album_item`): where
  they are and the ids Telegram needs to hand each file over again, never the
  text or the caption. A DM is always seen whole. A group is seen only while
  framy is an admin, which needs no right at all, so privacy mode stays on.
  Items are deleted 48 hours after framy first saw them, checked every hour.
  `Bot.Album` returns an album once no new photo has come for 1.5 s, at most
  a cap the caller sets, and `Bot.SeesEveryMessage` asks Telegram on every
  call whether framy is an admin of the chat, so the preview can say how much
  of the album it took. framy now also asks Telegram for `edited_message`,
  since an edit can swap the photo of an item.
- `/img` in groups and DMs, the first way to make an image from Telegram.
  `/img a prompt` alone makes one from scratch; in reply to a photo, an image
  document or a photo of an album, or as the caption of one's own photos, it
  edits them, up to 10 photos. framy answers the command with a preview
  card: the photos it takes, numbered, or the shape of the result; the maker
  and the model; the settings; and the price, as what is left of the free
  allowance or the estimate on the person's key. The caption is the prompt
  and what was left out. Nothing runs until the
  requester presses Generate, and No deletes the preview. Generate turns the
  preview into the generating loop, with the elapsed time in the caption and
  a Stop button; a result ready within 25 s replaces the loop in place, a
  slower one comes as a reply, and a failed run becomes a card that names the
  reason. Only the requester can press the buttons. A run is paid from the
  free allowance first (MAI Image 2.6 Flash up to 5 photos, FLUX.3 Klein up
  to 10) and from the person's own
  OpenRouter key when the allowance is used up or paused. framy keeps every
  run in `chat_run` (migration 014), so a restart picks it up where it was.
  `/edit` is in the group menu for video and answers that video in chats is
  not open yet. Again and Animate come later.
- A provider's refusal of a real person in a photo or a clip is its own
  error class, `faces`: Seedance on BytePlus answers so through OpenRouter.
  The chat says the model does not take real people and to try another
  model, in all sixteen languages, instead of "the model could not make it".
  A failed job's `error.message` is now the provider's own words, without
  framy's prefixes, the JSON OpenRouter wraps them in and the request id;
  the whole line stays in the job's error for the logs (migration 023).
- A provider's refusal of the sound a model made is its own error class,
  `sound`: Seedance 2.0 Mini with sound on failed with "the output audio may
  be related to copyright restrictions" and was not billed, and the same
  run without sound went through. The chat says to turn sound off and try
  again, in all sixteen languages, instead of "the model could not make
  it". Neither `faces` nor `sound` is recorded against the person's key any
  more, since neither says anything about it.
- The chat preview is a rich message: the card on top, the prompt quoted
  under it, then Model and Settings as sections that open in place, and
  Generate and No below. Model lists every model of the run's kind the way
  the site's picker does: makers A to Z, each under its name, a maker's
  families in the catalog's order and a family's variants strongest first,
  one button a model with what it would cost the run. No suggestions. A
  model the run cannot take stays in its place, disabled, with the reason:
  it needs a photo, takes none or fewer, needs a clip, or needs a key the
  person has not connected. Vector models are left to the site, since
  Telegram shows an SVG as a file. Settings, on the person's key, are the
  composer's first ones with the model's values: the shape, the resolution,
  the quality and the background, up to 4 results, a video's length and
  sound, and the size where a model has neither shape nor resolution. A
  video from a photo now picks its model and settings the same way, with the
  photo as its first frame where the model takes frames, and a model on the
  key starts from the site's defaults. A pick revises the draft and redraws
  the message; after a setting the settings stay open. Generate says what a
  run on the key costs. A preview already sent as a photo keeps its Model
  and Settings buttons until it expires, and so does a guest run. When framy
  is an admin of the group, or the command was ephemeral, the preview goes
  to the requester alone, and Generate posts the loop for the chat.
  Migration 015 keeps where the preview and the panel are.
- An upload that no job took within an hour is deleted, object and row,
  checked at start and every hour. The site will upload on Generate and the
  chat uploads right before its draft, so such a file is left from a
  failure. A file a job took, a cancelled draft's included, stays until the
  person deletes it. A job that tries to take a file at the moment the sweep
  deletes it is refused as if the file were not the person's.
- `R2_PREFIX` puts every object of a test bot under its own prefix, such as
  `test/`, so it can share the bucket without writing where production
  does. `deploy/ws04-test/compose.yaml` is the test bot's stack on ws04: its
  own PostgreSQL with the core stand-in, and the image built on the host
  from a commit of `dev` until there is a release. It pays the free
  allowance with the production key and counts its own budget, so it is
  stopped when nobody tests. Its site is `https://framy-test.ws04.cc`,
  through the ws04 Cloudflare tunnel, with no port published on the host.
  It checks webhook signatures with the signing secret of the OpenRouter
  workspace the production key belongs to.
- A free run that fails on framy's own OpenRouter key says the model could
  not make it, instead of blaming the person's key.
- `/img` is off, and the log says why, when there is no R2 bucket or ffmpeg
  lacks libx264. The loops and cards are kept in `ANIM_CACHE_DIR` (by default
  `framy-anim` in the system temp directory), outside `WORK_DIR`, which is
  swept every hour.
- Clips from the site, and videos that go on. `POST /api/videos/uploads`
  gives a presigned PUT into `incoming/` for one clip of up to 512 MiB, past
  Cloudflare's 100 MB per request; `POST /api/videos` then takes it, or cuts
  a part of a clip framy keeps, as a task the client polls at
  `GET /api/videos/{task}`, since encoding can outlast Cloudflare's 100 s.
  A clip is only remuxed when it is H.264 yuv420p up to 1920 px, 60 fps and
  10 Mbit/s with AAC or no sound, and encoded again otherwise; HDR comes to
  BT.709, a rotated clip comes upright, and its metadata, location included,
  goes. Its length, frame rate and sound are kept with the file (migration
  018), for uploads and for the videos providers make, so a run on a clip is
  checked and priced by what framy read, not by the browser. Until 31
  October a provider's video kept its length and frame rate in whole
  seconds and frames, cut down, and an upload its frame rate: 5.04 s was
  kept as 5, so a run on that clip was priced a little low.
  `POST /api/files/{id}/last-frame` keeps a clip's last frame as a photo, the
  frame before a fade when the last one is flat. A run names the job it
  continues with `continues`: framy fills `previous_job_id` or Omni's
  `previous_interaction_id` from that job, only the person's own finished
  job on the same model and provider, on their key, and prices the whole
  clip. A client that sends either setting itself is refused. The free edit
  on FLUX Video Edit now takes a clip of up to 5 s only, checked from the
  stored length in the queue's transaction.
- Continue joins the clips. A run that starts from a clip's last frame
  keeps its own video as the result and, beside it, the clip up to that
  frame with the new video after it, as one file: `joined` in the job, with
  its own link, download (`framy-<job>-joined.mp4`) and thumbnail. The two
  parts take the larger size and the higher frame rate, a part without
  sound gets silence, and the new video's first frame, which repeats the
  frame it started from, is dropped. Continuing the joined clip adds to all
  of it, up to 180 s, and `segments` says where each part begins, for the
  player to mark (migration 027). A join that fails leaves the run's video as it is.
  The frame remembers its clip (migration 026). Omni's own continuation is
  not joined, since its video may already hold the earlier one.
- `/edit` in a chat. In reply to a video, a GIF, a round video or a video
  document, or as the caption of one's own video, it edits the clip on FLUX
  Video Edit: the week's free video first, then the person's OpenRouter key.
  The free run takes the first 5 s of a longer clip and a run on the key up
  to 15 s, and the preview says so before Generate, so nothing is cut
  unseen. The preview card shows the clip's first frame and its length, with
  Generate and No. The result is remuxed with its index at the front and
  comes as a video Telegram streams, as a reply that notifies, and the loop
  goes. The week's limit is said in days and hours, and a refusal
  downloads nothing. `/img` on a video and `/edit` on a photo point to the
  other command, and the command menu calls `/edit` "Change a video".
- The length of a clip in chat is the person's choice. The free preview of a
  clip over 5 s offers all of it on the person's key, or the first 15 s past
  what FLUX Video Edit takes, marked 🔑 while no key is connected; the
  preview on the key offers the free 5 s back while the week's video is
  left. framy reads the clip from Telegram again for that, since only the
  part a run takes is kept (migration 019), and a draft's files can now be
  replaced in a revision, checked by the free allowance like new ones.
- Make a video and Make an image on a chat preview. A run of a prompt and at
  most one photo turns into the week's free video, HeyGen at 5 s and 768p
  (Grok Imagine Video Lite while the catalog lacks HeyGen), with the photo as
  its start frame, and back; past the week's video it runs on the person's
  key.
- Animate under an image result in chat: the image becomes the start frame
  of a new video draft with the same prompt. Its preview replies to the
  result, and in a group only the requester sees it, admin rights or not. A
  double tap makes one draft.
- A direct chat needs no command. Words, alone or with a photo, get the
  question "an image or a video", and the answer starts `/img` or the free
  video with the message as the prompt. Words with a clip go straight to
  `/edit` and an album with words to `/img`; a photo or a clip without words
  gets a line on what to send. A reply to framy's preview takes none of its
  pictures, a reply to a result takes the result.
- A video result in chat opens on a cover: the frame its preview card shows,
  the first that is not one colour, sent as a JPEG beside the video, so every
  client shows the same picture before it plays. A frame Telegram would not
  take as a photo, or a cover it refuses, leaves the video without one rather
  than holding it back.
- Results made on the site arrive in the person's DM when the bot may write
  there: the person allowed it at sign-in (`telegram:bot_access`) or pressed
  `/start`. framy checks with a chat action before any upload, so a person
  who allowed neither costs one call and gets nothing. An image comes with
  Animate and works as any result in the DM, several pictures come as one
  album, and a video with its cover. Each result goes at most once, a
  restart included (migration 020), and one that ended over an hour ago is
  not sent.
- Inline mode: `@framyin_bot` in any chat lists the person's results that
  went out as a photo or a video, newest first, 20 at a time, and typing
  filters them by the prompt. A result is sent again by its `file_id`, with
  its caption, so nothing is uploaded. The button above the results opens
  the bot's chat, whose panel opens the Mini App signed in: a Mini App
  opened straight from inline mode gets no sign-in data from Telegram and
  asked the person to sign in again. framy now records what kind of file each result went out as
  (migration 021); a result from before that is not listed. Inline mode has
  to be turned on in BotFather (`/setinline`).
- Each result in the job API has a `download` link that saves the file as
  `framy-<job>-<n>.<ext>`, so the site and the Mini App save a result under
  framy's name rather than the bucket's key.
- Send to Telegram from the Mini App: `POST /api/files/{id}/share` saves a
  result as a prepared inline message, and the Mini App hands it to
  `shareMessage`, so the person sends it to any chat, framy in it or not. A
  result that went out in a chat goes by its `file_id`; any other goes by a
  link Telegram fetches from framy, which makes a JPEG of a picture and
  passes an MP4 on as it is. The links are signed and last an hour. A video
  over 20 MB or a WebM cannot go and gets `not_shareable`. framy now builds
  on tg `dev` (fe692f2) for `savePreparedInlineMessage`.
- Guest mode: `@framyin_bot …` works in a chat framy is not in, in reply to
  a photo or not. The run is the one a group gets, in a single message: the
  preview's text and buttons with the card above them as a link preview,
  then the loop with the time and Stop, then the result or the error card,
  each an edit of the answer. Telegram takes no upload there, so the cards,
  the loop and the result go by links Telegram fetches from framy, signed
  as Send to Telegram's are; without an https `SITE_URL` a guest message
  gets no answer. A video result over 20 MB stays in the history, and the
  answer says so. An inline message cannot be deleted, so No and an expired
  preview shrink the answer to one line, "Cancelled.", and Stop leaves the
  card the run started from, "Stopped.". A reply to framy's answer while it
  still has buttons gets a line to use them instead of a second run. Only
  the person is recorded, with their own language, not the chat. Migration
  022 adds the `guest` surface and the run's `inline_message_id`. framy
  builds on tg `dev` (5e0a6c2) for `answerGuestQuery` and the text preview.
  Guest Mode has to be turned on in BotFather, and the live test (T9) is
  still to run.
- A result has a thumbnail for the site's grids: a WebP at most 480 px on
  the long side, of a video its poster frame, kept next to the result in R2
  and shown in the API as `thumb: {url, width, height}`. framy makes it when
  it keeps the result; a result kept before thumbnails gets one in the
  background, 20 a minute, newest first, and a file it cannot read is tried
  three times and then left without. Deleting a result deletes its
  thumbnail, and the thumbnails count towards what the person keeps
  (migration 024). The image's ffmpeg has the WebP encoder; without it
  framy makes no thumbnails and keeps the results as before.
- More of the site's settings. `GET /api/me` tells whether the language is
  the person's own pick (`language_chosen`), what framy's paid runs on each
  key cost since it was saved, in all and over the last 30 days, in the
  person's currency, the default image and video models and whether results
  made on the site go to the DM. `PATCH /api/me` sets the default models,
  checked against the catalog, and turns that DM on or off; a result that
  ends while it is off is not sent later. Migration 025 adds the columns to
  `profile` and the `off` end of a site delivery.
- A link to a person's file is the same for a whole hour: it is signed at
  the start of the hour, lives 10 minutes past it, and its answer carries
  `Cache-Control: private, max-age=4200`, so the browser shows a file it has
  seen from its cache instead of fetching it on every page.
- CI on every push and weekly: modules, gofmt, build, vet, race tests,
  database tests on PostgreSQL 17, govulncheck, the image build, the photo
  and storage tests inside the image, on the ffmpeg it ships, and the local
  Compose file. A tag publishes the image to GHCR and the GitHub Release only
  after CI passed on the tagged commit. Dependabot opens grouped updates
  against `dev`.

- `docs/design.md`: what framy is, the surfaces (site, Mini App, groups, DMs),
  accounts through Telegram, providers and people's keys, the model catalog,
  prices in USD and the person's currency, the free allowance, jobs, `/img`
  and `/edit` in a group with albums, storage in Cloudflare R2, security and
  operations, the changes framy needs in `tg`, and the stages.
- `docs/research/`: the research of 26 October 2026 behind the design, in
  Russian, with sources: image and video models, providers, Telegram, and how
  to build and run the service.
