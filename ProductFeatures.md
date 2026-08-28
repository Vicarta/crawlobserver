# CrawlObserver: каталог функціональності

Актуальність: 2026-08-28.

Цей файл є канонічним каталогом фактично реалізованої функціональності
CrawlObserver. Він описує можливості продукту на рівні користувацьких сценаріїв,
API, crawler engine, аналітики та експлуатації.

## Правило підтримки документа

- Кожна зміна, яка додає, змінює або видаляє функціональність, повинна в тому
  самому наборі змін оновлювати цей файл.
- Це стосується UI, API, CLI, crawler/fetcher/parser/renderer, звітів,
  інтеграцій, моделей даних, доступу, deployment та operational tooling.
- Внутрішній рефакторинг без зміни поведінки не потребує оновлення каталогу.
- Не описувати заплановані можливості як реалізовані. Майбутні роботи належать
  до `.planning/`, а не до цього документа.

## 1. Призначення продукту

CrawlObserver - система для технічного SEO-аудиту, регулярного моніторингу
сайтів і аналізу внутрішнього посилального графа. Продукт поєднує crawler,
ClickHouse-сховище, вебінтерфейс, REST API, CLI та desktop GUI.

Основні аудиторії:

- SEO-фахівці та SEO-команди;
- власники і технічні команди сайтів;
- агентства, що ведуть кілька проєктів;
- зовнішні агенти й автоматизації, які читають crawl data через API;
- адміністратори приватного self-hosted deployment.

## 2. Ролі, автентифікація та доступ

- Basic Auth для сумісного адміністративного доступу.
- Локальні користувачі з cookie-сесіями та login/logout workflow.
- Ролі `admin` і `viewer`.
- Призначення користувачів на конкретні проєкти.
- Проєктна ізоляція списків проєктів, crawl sessions і session data.
- General API keys для адміністративних та cross-project операцій.
- Project API keys для обмеженого read-only доступу до одного проєкту.
- Project-bound targeted rescan API використовує окремий Project API key із
  privileged capability `targeted_rescan`; evidence-only Project API keys не
  отримують write access.
- Створення, перегляд і відкликання API keys через UI та API.
- Керування користувачами через адміністративний UI та API.
- Rate limiting для загальних і authentication endpoints.
- Public health, theme, setup status та login endpoints; інші API маршрути
  проходять authentication/authorization middleware.

## 3. Проєкти та сесії

### Проєкти

- Створення, перейменування і видалення проєктів.
- Видалення проєкту окремо або разом із його crawl sessions.
- Прив’язування, відв’язування та batch assignment сесій до проєктів.
- Список unassigned sessions із загальною кількістю, session label/status і
  датою запуску для розрізнення історичних, verification та звичайних crawl.
- Лічильники сторінок проєктів у sidebar.
- Проєктні вкладки: Sessions, Search Console, Daily Delta, Quality, Settings та
  підключені SEO data providers.
- Project Current Snapshot і Current Baseline Snapshot з прямою навігацією.

### Crawl sessions

- Створення нового crawl з одним або кількома seed URL.
- Вибраний проєкт перевіряється сервером до створення crawl session; невідомий
  `project_id` відхиляється, а порожнє значення нормалізується як unassigned.
- Live progress через Server-Sent Events.
- Статуси queued, running, completed, failed і stopped.
- Явні причини зупинки, зокрема manual stop та interruption by restart.
- Зупинка активного crawl.
- Project-bound targeted rescan перевіряє project/session binding і точний
  allowed origin за canonical seed URL сесії до будь-якої mutation. Запит
  обмежений 200 absolute HTTP(S) URL, вимагає `Idempotency-Key`, а durable
  SQLite ledger повертає той самий audit result для ідентичного retry та
  відхиляє повторне використання ключа з іншим payload. Новий route приймає
  лише project API key з capability `targeted_rescan`, жорстко звіряє його
  `project_id` з path і не розширює права evidence-only project keys;
  general/admin доступ збережений тільки для legacy session-only route.
- Targeted-rescan receipt є audit receipt, а не підтвердженням публікації.
  Post-rescan verification окремим project read key спочатку читає trusted
  Current Snapshot і вимагає, щоб `current_session_id` дорівнював
  `receipt.session_id`, читає саме цю terminal session через session GET, а
  потім перевіряє exact URL та свіжий `CrawledAt` у page detail. Targeted
  rescan змінює наявну session in place і сам не створює та не промотує новий
  trusted snapshot. Mismatch, stale, untrusted і malformed результати fail
  closed як not verified; контракт зафіксований versioned executable fixture
  та real-handler integration test.
- Resume незавершеної сесії з попередніми параметрами.
- Якщо параметри Resume змінені, UI вимагає підтвердити Full Recrawl.
- Retry failed pages за status code.
- Rescan вибраних сторінок у наявній сесії.
- Перейменування session label.
- Видалення окремої сесії та очищення unassigned sessions.
- Порівняння двох сесій за stats, pages і links.
- Export та import повної сесії.
- Import crawl data з CSV.
- Retention policy для кількості неактивних сесій на проєкт.
- Terminal session status публікується після recompute depth, Internal PageRank
  і near-duplicate analytics, щоб downstream quality gates читали завершені
  derived metrics.
- Session list/detail API додає response-only `effective_origin` та
  `effective_origin_state`: `proven` походить лише з durable launched-request
  і final-response evidence для кожного фактично запущеного URL, а
  `unavailable`/`ambiguous` ніколи не виводять origin із raw seed, canonical,
  sitemap, DNS або config. Exact raw `SeedURLs` залишаються незмінною audit
  provenance; Project Sessions показує proven operational origin і окремо
  підписаний raw seed.

## 4. Налаштування crawl

- Максимальна кількість сторінок і максимальна глибина.
- Кількість workers і delay між запитами.
- Scope: exact host, registrable domain або subdirectory.
- Один або кілька seed URL.
- Збереження HTML сторінок у стиснутому вигляді.
- Отримання sitemap та окремий sitemap-only crawl mode.
- Дотримання `robots.txt` або явне admin-controlled ігнорування.
- URL exclude patterns; посилання зберігаються навіть тоді, коли target не
  допускається до crawl.
- Перевірка зовнішніх посилань і окрема кількість external-link workers.
- Перевірка page resources: CSS, JavaScript, fonts, icons та images.
- Вибір User-Agent із presets для Googlebot, Bingbot, Chrome або custom value.
- TLS fingerprint profiles для browser-like network identity.
- Source IP binding та IPv4-only режим із попередньою перевіркою outbound IP.
- SSRF protection і блокування private/reserved IP за замовчуванням.
- HTTP timeout та максимальний розмір response body.
- Обмеження concurrent sessions, workers і frontier size.
- Retry policy з max retries, exponential backoff, consecutive-failure та
  global-error-rate guards.
- Optional extractor set, який запускається разом із crawl.

## 5. Fetching, frontier і crawl safety

- Concurrent fetch workers і thread-safe priority frontier.
- URL normalization та deduplication.
- Per-host queues, crawl delay і robots caching.
- Redirect tracking із hop-by-hop chain та final URL.
- HTTP status, response headers, content type/encoding, body size, fetch time та
  crawl timestamp.
- Conditional handling для transient failures і bounded retries.
- TLS profile selection і safe DNS dialing із захистом від DNS rebinding.
- robots.txt discovery, persistence та URL access testing.
- Sitemap index/urlset discovery, recursive traversal і raw `<loc>` evidence.
- Page depth та `found_on` calculation із подальшим graph recomputation.
- Daily Delta має незалежний exact discovery budget: planned candidates не
  витрачають його; лише успішне унікальне додавання static або rendered URL до
  frontier витрачає одну одиницю. Значення `0` вимикає link discovery, тоді як
  seeds і retries продовжують запускатися.
- Контрольована зупинка при crawl cancellation, process shutdown або критичних
  storage failures.
- Exposed progress, audit data, retry counters і recent status timeline.

## 6. JavaScript rendering

- Chromium-based rendering modes: off, auto і always.
- Detection сторінок, яким потрібен JavaScript rendering.
- Конфігуровані render workers і per-page timeout.
- Optional blocking images/fonts під час rendering.
- Per-origin render serialization, щоб паралельні SPA routes одного origin не
  змішували title, metadata або DOM state.
- Rendered DOM є authoritative source для SEO metadata, headings, content
  metrics, canonical, duplicate detection та link graph.
- Static response values зберігаються окремо для діагностики.
- Порівняння Static vs Rendered для title, meta description, H1, canonical,
  content, links, images і structured data.
- Render duration та render error per page.
- Optional discovery/following links, створених JavaScript.

## 7. Page signals і парсинг

Для кожної сторінки система зберігає та аналізує:

- original URL, final URL, status code і redirect chain;
- content type, encoding, body size, response headers і response time;
- title, Unicode-aware length, meta description, meta keywords;
- meta robots, X-Robots-Tag, canonical і self-canonical state;
- indexability та конкретну причину non-indexable state;
- H1-H6 і повний heading outline у document order;
- language, hreflang entries та hreflang validation;
- Open Graph title, description та image;
- schema.org / JSON-LD types, validation errors і warnings;
- word count і content hash;
- page-created та page-modified dates, коли їх можна надійно витягнути;
- images count та images without alt;
- internal/external outgoing links, anchors, rel, HTML tag і DOM location;
- incoming internal link count, depth і Internal PageRank;
- HTML body, якщо ввімкнене зберігання HTML;
- static/rendered variants і change flags для JavaScript-rendered pages;
- Core Web Vitals Lab measurements, якщо їх увімкнено.

## 8. URL provenance і діагностика сторінки

URL Detail містить:

- status, content type, size, response time, depth, PageRank і crawl time;
- response headers;
- SEO, Open Graph, headings hierarchy та structured data;
- content і link metrics;
- JS Rendering та Static vs Rendered comparison;
- Core Web Vitals Lab data;
- outbound links, inbound links та backlinks;
- stored HTML preview, якщо HTML доступний;
- GSC ranking keywords для конкретної сторінки.

Блок `URL discovered from` завжди показується перед GSC і пояснює, чому URL
присутній у crawl:

- direct internal referrer;
- internal referrer через redirect alias;
- source page, original target, anchor, rel, tag і DOM location;
- sitemap URL і raw `<loc>`;
- crawl seed;
- Daily Delta candidate sources;
- retained `found_on` evidence;
- explicit unavailable state для legacy rows без достатньої provenance.

Pages і URL Detail використовують спільну source classification, тому final URL
після redirect не повинен помилково виглядати orphan URL.

## 9. Reports

### Overview

- Total pages, internal/external links і unique external domains.
- Error count, average response time, pages per second, duration і max depth.
- Status-code distribution і деталізація 2xx/3xx/4xx/5xx/connection errors.
- Retry та fetch-error statistics.
- Crawl depth distribution.
- Top pages by Internal PageRank.
- Full і recent status timeline.

### Content

- Missing, short, long і duplicate titles.
- Shared rendered/static metadata warnings.
- Missing, short і long meta descriptions.
- Missing або multiple H1.
- Content length distribution.
- Image totals, images without alt і affected pages.
- Drilldowns із report cards у відфільтровані Pages views.

### Technical

- Indexable/non-indexable pages і reason distribution.
- Self, other і missing canonicals.
- Redirect pages, long redirect chains і error pages.
- Soft 404 та shared rendered metadata shell findings.
- Response-time і content-type distributions.
- Crawl status timelines і retry/fetch-error diagnostics.
- Compact Core Web Vitals summary із переходом у повний CWV report.

### Links

- Internal та external link totals.
- Pages without outlinks, pages with excessive outlinks і broken internal links.
- Dofollow/nofollow distribution.
- Top external domains і anchors.

### Structure

- URL directory distribution.
- Crawl depth distribution.
- Orphan-page count.

### Sitemaps

- URLs present both in crawl and sitemap.
- Crawled-only та sitemap-only URLs.
- Sitemap coverage totals і drilldowns.

### International

- Structured-data type distribution.
- Language distribution.
- Hreflang та HTML language coverage.

### Core Web Vitals / Lab data

- Окремий report після International і summary у Technical report.
- Page-level LCP, CLS та TTFB.
- Good, Needs Improvement і Poor ratings per metric та overall.
- Eligible, measured, unmeasured і rating summary counts.
- Rating filter, sorting, pagination та URL Detail navigation.
- Це lab data конкретного crawl, а не Chrome UX Report field data.

## 10. Pages explorer

Views:

- All pages і HTML pages;
- Titles, Meta, H1/H2, Images;
- Issues;
- Indexability;
- Response;
- Redirects;
- Near Duplicates;
- Hreflang validation;
- URL Patterns, Parameters, Directories і Hosts.

Можливості таблиць:

- server-side filtering, sorting і pagination;
- `!value` syntax для exclusion у text filters;
- persisted resizable columns;
- overflow tooltips для довгих URL і titles;
- перехід у URL Detail та відкриття URL у браузері;
- перегляд stored HTML;
- CSV export відфільтрованого набору;
- selection і export selected rows для selectable tables;
- admin rescan та delete selected pages;
- ручний recompute depth, PageRank, near duplicates і hreflang validation.

Generic Issues включають soft 404, duplicated rendered titles/static metadata та
shared rendered metadata shell diagnostics без site-specific правил.

## 11. Directives і sitemap tools

- Перегляд robots.txt для всіх crawled hosts.
- Перегляд status і raw robots content.
- Тест доступності конкретного URL за поточними directives.
- Симуляція альтернативного robots.txt без зміни сайту.
- Sitemap tree з index/urlset relationships.
- Перегляд URL і metadata sitemap entries.
- Coverage views: sitemap only, in both і crawl only.
- Fresh sitemap observations для Daily Delta з declared/fetched roots, counts,
  added/removed URLs, warnings та raw evidence.

## 12. Resources

- Summary resource checks за типом і status.
- URL-level список CSS, JavaScript, fonts, icons та images.
- Internal/external classification.
- Status-code, redirect і error diagnostics.
- Filtering, sorting, pagination та CSV export.
- Reparse resources із stored HTML для сумісних historical sessions.

## 13. Links, external checks і backlinks

### Internal links

- Один рядок на source-target edge.
- Source, target, anchor і tag.
- Filtering, sorting, pagination, URL Detail navigation та CSV export.

### External links

- Raw outbound links із source URL, external target, anchor, rel, tag та
  location.
- Окремі URL checks і domain summaries.
- Status, redirect та expired-domain diagnostics.
- Current Snapshot використовує фактичні materialized raw graph edges.

### Backlinks та authority

- Optional provider-backed backlink view.
- Source/target URLs, anchors, Trust Flow та topical data, коли provider це
  повертає.
- Session authority summary для прив’язаного проєкту.

## 14. Internal PageRank

- In-memory PageRank calculation для internal graph.
- Damping, convergence і normalized logarithmic score 0-100.
- External links та nofollow/sponsored/UGC враховуються як dilution.
- Nofollow/sponsored/UGC не передають rank.
- Self-links виключаються.
- Redirect targets консолідуються з hop loss; canonical consolidation не має
  redirect penalty.
- Optional inclusion/exclusion footer links за DOM location/selectors.
- Views: Top pages, Directory/treemap, Distribution і paginated Table.
- Weighted PageRank view.
- Quality Gate, distribution, treemap, top pages і weighted reports працюють з
  одним versioned eligible-page predicate та exact evidence revision.
- PageRank evidence зберігає attempt/event sequence, source, algorithm/options,
  predicate version, graph/rank fingerprints і eligible/positive/zero counts.
- Звіти fail-closed, якщо найновіша спроба `started`/`failed`, predicate
  застарів або FINAL page population/revision/fingerprint не відповідає evidence.
- Static і JavaScript-rendered crawl публікують terminal status та закривають
  `Done` лише після durable finalized PageRank evidence; failure завершується як
  `completed_with_errors`.
- Manual recomputation і project-level recompute status.
- Automatic recomputation після graph-changing Current Snapshot updates та
  відповідних project settings changes.
- Єдиний versioned eligibility predicate `pagerank-eligible-v1` використовується
  для розрахунку, звітів і quality checks: HTML 2xx без followed redirect та з
  порожнім або self canonical; `noindex` не виключає сторінку з graph.
- Кожна спроба розрахунку має append-only evidence зі станом `started`,
  `finalized` або `failed`, джерелом, версіями алгоритму/predicate, fingerprint
  graph/rank та кількістю eligible/positive/zero сторінок.
- `finalized` публікується лише після `FINAL`-перевірки synchronous ClickHouse
  mutation; новіша pending/failed evidence закриває доступ до старої finalized
  revision для downstream trust decisions.
- Історичній завершеній сесії можна детерміновано додати
  `observed_existing` evidence після подвійної read-only перевірки наявних rank;
  це не запускає PageRank recompute і не змінює сторінки сесії.

## 15. Interlinking і PageRank Lab

- TF-IDF-based пошук internal-link opportunities.
- Opportunity і cannibalization categories.
- Sorting, filtering, pagination, row selection і selected CSV export.
- Симуляція додавання запропонованих links.
- Import virtual links для simulation.
- Збереження і перегляд simulation history/results.
- PageRank impact before/after/diff.
- Окрема PageRank Lab працює з Current Snapshot проєкту.
- Симуляція як додавання, так і видалення existing links.
- Validation того, що link, який видаляється, існує у graph.
- Focused impact для безпосередньо affected pages і повна paginated results table.

## 16. Near duplicates, hreflang і URL structure

- SimHash/content-hash based near-duplicate detection.
- Manual recomputation content hashes для historical data.
- Hreflang validation і validation rerun.
- URL pattern aggregation.
- Query-parameter inventory.
- Directory і host aggregation.
- Redirect-page analysis.

## 17. Custom tests і custom extraction

### Custom tests

- CRUD rulesets.
- Правила з configurable conditions і severity.
- Запуск ruleset проти вибраної crawl session.
- Перегляд результатів у Tools > Tests.

### Extraction

- CRUD extractor sets.
- Запуск extraction проти stored page data.
- Використання extractor set безпосередньо під час нового crawl.
- Перегляд extraction results у Tools > Extractions.

## 18. Daily Delta Crawl

- Scheduled daily crawl у configurable time і timezone.
- Manual Run now і non-mutating Preview.
- Candidate sources: fresh sitemap, GSC, problem pages, stale pages і manual
  queue.
- Fresh sitemap refresh перед preview/run/scheduled plan.
- Explicit skip або labelled snapshot fallback, якщо sitemap refresh неуспішний.
- Candidate source counts, launch counts, deferred counts і launch limit.
- Limits для candidates, changed pages, new pages, discovered pages, discovery
  depth і runtime.
- Follow internal links із new/changed pages у межах configured limits.
- Rate limit, retries, backoff і JS rendering для Delta.
- robots compliance, conditional GET requests з exact retained `ETag` і
  `Last-Modified` validators. Якщо server
  відповідає `304 Not Modified`, Delta зберігає raw response evidence без
  parsing/rendering/resource/extraction/link-discovery work.
- URL policy: canonical host, trailing slash, fragments, tracking/query params,
  allowed params та allow/block patterns.
- Confirmation policies для scope change і full recrawl.
- Пауза Delta, коли full crawl активний.
- Previous snapshot не видаляється до успішного завершення нового run.
- Candidate plan і sitemap provenance зберігаються для audit та quality gates.
- Changed-only sitemap selection compares fresh evidence with the published
  Current Snapshot safety term: only added URLs and strictly forward valid W3C
  `lastmod` values become events; raw unpublished observations only label retry
  provenance and cannot consume a pending event.
- Sitemap selection prioritizes evidence-backed changed events, then up to 50
  deterministic rotating canaries; the existing changed/new limits and
  `max_candidates_per_run` remain configurable safety ceilings rather than
  daily targets. Event/canary/deferred
  counts, rotation epoch, selector revision, published/raw observation lineage,
  selection completeness, and per-URL source remain durable Delta plan audit
  evidence; legacy plans explicitly retain absent selection provenance. Existing
  project maximums remain unchanged. Canaries fill remaining capacity
  after changed, manual, and problem candidates instead of displacing them;
  changed-event, canary, and global candidate limits are editable in Daily Delta.
- Selector v2 can suppress only an exact redundant refetch after two distinct
  completed fresh raw observations for the same project prove the same normalized
  URL, valid `lastmod`, terminal page evidence, and equal nonzero content hash.
  This is read-only execution evidence: it records proof-pair lineage/digest,
  stable versus actionable published differences, and keeps Current Snapshot
  sitemap publication explicitly held. Missing, invalid, changed, or zero-hash
  evidence remains actionable; the first bulk change is never auto-acknowledged.
  Saved candidate limits remain upper safety ceilings, not a 50-80 URL target:
  a genuine site-wide change can schedule most or all affected pages up to those
  ceilings, regardless of cohort size.

## 19. Current Snapshot

- Materialized full-site snapshot на рівні проєкту.
- Trusted full crawl стає baseline.
- Trusted Daily Delta updates fold у current state; raw Delta `304 Not Modified`
  rows лишаються audit/coverage evidence, але не overlay-ять materialized page
  або source-link evidence Current Snapshot.
- Failed/untrusted Delta не публікується.
- Raw crawl sessions залишаються audit artifacts.
- Configurable maximum retained deltas і baseline fold interval.
- Full-crawl promotion створює новий materialized snapshot; Delta overlay
  серіалізується per project і має durable content-stage/pointer recovery.
- Recalculation Internal PageRank після trusted graph update.
- Current Snapshot і Current Baseline Snapshot сумісні з session analytics API/UI.
- Кожна promotion зберігає provenance binding до конкретних quality evaluation,
  PageRank evidence, rules/evaluator і baseline revisions та власний статус.
- Full і Delta promotion мають монотонний authoritative content watermark
  `(started_at, session_id)`: повторне оцінювання історичної сесії отримує
  статус `superseded` і не може відкотити Current Snapshot до старішого crawl.
- Кожен Delta plan незмінно фіксує materialized snapshot revision, raw full
  source, content watermark та обидві quality evaluation revisions. Повторна
  оцінка застосованого Delta перевіряє ці exact journal facts, а не поточні
  quality pointers; відсутня або суперечлива lineage блокує promotion як
  `stale_delta_baseline` і вимагає нового plan.
- Delta preview/run тримає спільний per-project snapshot lock від canonical
  lineage capture до завершення всіх candidate reads, тому concurrent promotion
  не може змішати metadata однієї revision з content іншої. Admin PageRank,
  orphan cleanup та project deletion використовують той самий mutation lock;
  cleanup не відпускає його між graph delete і PageRank finalization.
- Після Delta fold raw predecessor session і важкий crawl payload видаляються,
  тому scheduler їх більше не переоцінює; exact immutable quality/PageRank
  evidence, потрібні live DeltaPlan journal lineage, залишаються доступними.
  Restart replay поточного Delta не змінює його evaluation revision або Current
  Snapshot binding.
- Unprovable pre-25.1 Current Snapshot лишається fail-closed для GET і Delta.
  Admin re-evaluate новішого trusted full crawl може відновити його без ручної
  зміни БД: storage приймає лише full self-baseline binding, залишає legacy row
  audit-only і публікує повністю доказовий v2 pointer.
- Якщо full snapshot pointer вже durable, але процес завершився до terminal
  promotion audit, retry повертає той самий canonical pointer і дописує
  `applied` для початкового promotion ID без нової evaluation чи повторного copy.
- Promotion перевіряє binding перед публікацією, відхиляє missing/pending/failed
  або stale PageRank evidence і може безпечно повторити лише незавершений крок
  promotion без повторного quality evaluation.
- `serve`, `gui`, `crawl` і `migrate` використовують process-lifetime OS lock у
  спільному state-каталозі: другий writer завершується до будь-якої міграції чи
  фонового job, а deploy/migrate спочатку проходить active-crawl safety gate.
- Current Snapshot pointer має монотонну revision; publish/readback завершується
  до cleanup старого baseline/delta state, а незавершений cleanup безпечно
  відтворюється після restart без повторного накладання Delta content.
- Read API fail-closed перевіряє не лише збережений binding, а й актуальні
  evaluator/rules, current quality revision, newest PageRank evidence і lineage
  quality-baseline сесії.
- Sitemap membership оновлюється лише після trusted fresh promotion.
- Перед будь-якою Current Snapshot mutation Delta promotion повторно звіряє
  selector revision, complete fresh observation та exact published
  `current_session_id`/snapshot revision/content watermark із plan. Stale або
  mixed lineage повертається як superseded без overlay; trusted incomplete
  selection може оновити selected pages, але залишає published sitemap term
  byte-equivalent, тож deferred events залишаються pending для наступного plan.
- Safe orphan 404 cleanup видаляє лише stale 404 із current snapshot, якщо немає
  internal inlinks і sitemap membership; raw sessions не видаляються.

## 20. Crawl Quality Trust Gate

- Session-level result: trusted, warning або untrusted, score і findings.
- Порівняння full crawl з останнім trusted full baseline.
- Coverage drop/growth thresholds.
- Internal-link drop threshold.
- Growth thresholds для 404, 5xx, noindex, redirects і canonical mismatch.
- PageRank top-page overlap і zero-PageRank checks.
- Stable canary URLs з expected status, final URL, canonical, title fragment,
  minimum internal links та indexability expectations.
- Окремі Daily Delta gates для crawled/launch coverage, sitemap candidate size,
  candidate representation та 5xx share.
- Blocking gates не дозволяють bad crawl/delta змінити Current Snapshot.
- Project Quality UI для thresholds, canaries і result details.
- Quality status доступний через API для зовнішніх агентів.
- Quality evaluation та її findings зберігаються як immutable revision; окремий
  current pointer публікується лише після повного durable запису обох наборів.
- Evaluation revision детерміновано зв'язує session, PageRank evidence,
  eligibility predicate, evaluator/rules і baseline revisions; scheduler
  переоцінює при будь-якій зміні lineage, включно з покращенням або погіршенням.
- Evaluator revision залежить від типу сесії: full crawl зберігає v2 identity,
  а Daily Delta використовує v3 із fail-closed `stale_delta_baseline`. Тому
  старі Delta v2 verdicts стають stale і переходять на окрему immutable v3
  evaluation, не змінюючи full-baseline revisions; повторний scheduler replay
  працює ідемпотентно без finding-count collision.
- Scheduler використовує bounded fair scan, тому старі stale/missing сесії не
  блокуються новішими no-op результатами; restart змінює scan offset
  детерміновано і не порушує idempotency.
- Legacy quality result і повний набір findings імпортуються в history без
  перезапису, тому попередній stale verdict залишається доступним для audit.
- Quality details показують evaluated time, evaluation/evidence revisions,
  source/status, eligible/positive/zero counts, rules/evaluator/baseline,
  stale reasons та Current Snapshot promotion status.
- Admin може синхронно й ідемпотентно переоцінити завершену сесію через API/UI
  з explicit confirmation, audit reason та optimistic revision checks; операція
  не створює crawl, не перераховує PageRank і не послаблює thresholds.
- Після успішного re-evaluate modal і session badge перечитують authoritative
  quality, immutable history та PageRank evidence через GET; частковий POST
  result не може тимчасово приховати наявні findings. Idempotent response
  проходить той самий refresh path; 409 оновлює authoritative quality без
  success-state, а інші помилки зберігають попередній повний modal/badge state.
- Admin repair має durable `started -> applied/failed` audit lifecycle з
  монотонною sequence, redaction credential-like values і fail-closed записом
  до будь-якої quality/snapshot mutation.

## 21. Google Search Console

- OAuth connection на рівні проєкту.
- Вибір GSC property і зміна property без обов’язкового disconnect.
- Manual data fetch, progress/stop і disconnect.
- Overview metrics.
- Queries, Pages, Countries і Devices.
- Timeline із date filtering.
- URL inspection data.
- Ranking queries для конкретного URL у URL Detail.
- GSC може бути candidate source для Daily Delta.

## 22. SEO data providers

- Project-level provider connection framework.
- Реалізований SEObserver provider client.
- Secure API key connection і domain association.
- Background fetch із progress, stop, cache window та force refresh.
- Domain metrics, backlinks, referring domains, rankings, visibility history і
  top pages.
- Provider API-call log і configurable fetch limits.
- Provider data доступні через project UI, Authority/Backlinks views та REST API.
- Provider UI позначений як premium/optional і потребує чинного provider account.

## 23. Import, export і API

- REST API для проєктів, сесій, pages, links, reports, directives, resources,
  PageRank, GSC, providers, quality, Delta, custom tests, extractions і admin.
- Readback API для current/immutable quality history і PageRank evidence, а
  також admin-only idempotent quality re-evaluation із typed conflict response.
- Health і server-info endpoints.
- Server-side pagination, filtering та sorting для великих datasets.
- Text exclusion filter через leading `!`.
- Session export/import і CSV import.
- CSV exports із Pages, Links, Resources та аналітичних tables.
- Export selected rows у Pages, Sessions та Interlinking Opportunities.
- Critical-data export для recovery.
- External-agent guide у `docs/crawlobserver-api-agent-guide.md`.

## 24. Global UI та адміністрування

- Dashboard і глобальний список crawl sessions.
- Пошук проєктів у sidebar.
- Global statistics page.
- Storage usage per session.
- Runtime system stats: memory, goroutines і GC data.
- Settings UI для crawler/server/theme/telemetry та інших config groups.
- Custom app name, logo, accent color, light/dark mode.
- First-run onboarding wizard.
- API management і user management.
- Application logs із filtering та export.
- In-app announcements із remote feed та opt-out settings.
- Optional anonymous telemetry та окремий explicit session-recording consent.
- Responsive desktop/mobile layouts, keyboard-oriented table controls і
  localized labels.
- Переклади: English, Arabic, German, Spanish, French, Hebrew, Indonesian,
  Italian, Japanese, Korean, Dutch, Polish, Portuguese, Russian, Turkish і
  Chinese.

## 25. CLI і desktop GUI

CLI commands:

- `crawlobserver crawl` - start crawl;
- `crawlobserver serve` - start web server/UI;
- `crawlobserver gui` - start desktop GUI;
- `crawlobserver sessions` - list sessions;
- `crawlobserver report external-links` - table або CSV report;
- `crawlobserver migrate` - create/update ClickHouse schema;
- `crawlobserver install-clickhouse` - install managed ClickHouse binary;
- `crawlobserver update` - check/apply self-update;
- `crawlobserver version` - print version.

Configuration підтримує YAML, `CRAWLOBSERVER_*` environment variables і CLI
flags. Web UI компілюється в Go binary, тому production не потребує Node.js.

## 26. Storage, backup та recovery

- ClickHouse columnar storage, partitioned by crawl session.
- Managed ClickHouse mode або external ClickHouse connection.
- Batch inserts і configurable flush interval.
- ZSTD-compressed stored HTML.
- Automatic schema migrations.
- SQLite store для users, API keys, project settings та integration metadata.
- Manual backup list/create/restore/delete через API/UI.
- Scheduled backups за замовчуванням виконуються раз на 24 години та зберігають
  дві останні копії; interval і retention залишаються configurable. App restart
  не створює зайву копію, якщо останній scheduled archive ще не прострочений.
- Critical-data export є окремим recovery-шляхом. Scheduled full archive зберігає
  DDL `gsc_analytics`, але не дублює її rows, які входять до critical export;
  manual full backup залишається самодостатнім і містить усі таблиці.
- Session retention scheduler.
- ClickHouse працює з logger level `information`; `trace_log`,
  `processors_profile_log` і невикористаний OpenTelemetry span log не
  зберігаються постійно. Решта operational system logs мають
  триденний TTL, файлові логи обмежені 100 MB і трьома ротаціями.
- Docker JSON logs app і ClickHouse обмежені трьома файлами по 20 MB.
- Host-side ClickHouse log rotation, compression і триденний retention для
  Docker deployment без restart ClickHouse.

## 27. Deployment та operations

- Docker image і Docker Compose stack для app, ClickHouse та migration tool.
- App доступний через loopback binding; ClickHouse не потребує published host
  ports.
- Tailscale-only deployment pattern.
- Health endpoint `/api/health`.
- Versioned GHCR images і GitHub Actions build workflow.
- App-only deploy та rollback scripts із `--no-deps`.
- Safe restart guard перевіряє active/queued crawl sessions і відмовляється
  перезапускати app за замовчуванням.
- Read-only `CHECK_ONLY=1` preflight.
- Production правило: не перезапускати ClickHouse під час app rollout.
- Automatic migrations, startup logging і recent-log inspection.
- Runtime resource limits для memory та GOMAXPROCS.
- Self-update status і apply flow для supported installations.

## 28. Відомі межі

- Core Web Vitals report містить lab data з crawl environment, а не real-user
  CrUX field data.
- Provider-backed Authority/Backlinks/Rankings функції залежать від зовнішнього
  provider account та API availability.
- Historical sessions, створені до збереження певної provenance, можуть мати
  explicit `unavailable` замість вигаданого discovery source.
- JavaScript rendering потребує доступного Chromium binary.
- Збережений HTML і повторний reparse доступні лише для sessions, де HTML був
  збережений або відповідні дані вже були persisted.
- Current Snapshot публікує лише trusted data; raw failed/untrusted sessions
  залишаються видимими окремо для audit.
