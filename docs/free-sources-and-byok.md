<!--
Generated 2026-08-09 by a multi-agent research pass verified against live pages and
endpoints on that date. Two subjects: (1) free, current content sources: the
daily-Kaggle-tweets idea, live keyless APIs (Bluesky, GDELT, Lemmy), and the HF
hacker-news archive; (2) the caller-supplied-key (BYOK) design, including the per-provider
ToS verdicts that fixed the BYOK surface at YouTube + Mastodon only.

The corpus expansion proposed in section 3 (Bluesky, GDELT headlines, HN backfill joining
HN+RSS) was a PROPOSED AMENDMENT when written. That sign-off happened on 2026-08-10:
docs/corpus-policy.md is now the single source of truth for training eligibility, and
section 6 below records what has shipped since. ToS drift is the norm; re-verify before
relying. Engineering research, not legal advice.
-->

# Sentilyzer: Free Sources + BYOK (Synthesis)
*All claims verified against live pages/endpoints 2026-08-09 unless marked [likely] (secondhand) or [unverified].*

---

## 1. Verdict

The daily-Kaggle-tweets idea is a mirage: every "Daily Updated" tweets dataset on Kaggle died when X killed free API access in 2023 (last real updates 2023-02-28 and 2023-06-29; two siblings gutted to 22-byte husks), and any tweet mirror is poisoned regardless of its label because the X Developer Agreement bans training (III.A(k)) and redistribution/derivatives (III.A(d)). A scraper's CC0 tag grants nothing X withholds. What is real and free, ranked: (1) **Bluesky**: keyless topic search plus a free firehose, robots.txt that explicitly invites crawling, and the only realistic candidate to join HN+RSS in the training corpus; (2) **GDELT DOC 2.0**: keyless 15-minute news metadata + precomputed tone, the only source surveyed whose license affirmatively grants unlimited commercial use and redistribution; (3) **HF open-index/hacker-news**: the complete HN archive live-updated every 5 minutes, a 20-year training backfill for an already-whitelisted source; (4) **Lemmy**: keyless and live, serving-only for now. For BYOK, adopt the Portkey/LiteLLM per-request-header pattern: namespaced `X-Sentilyzer-*` headers (never URL, never body), mirrored as lowercase gRPC metadata to the worker, memory-only, never logged, with the serving cache partitioned by credential hash. Two providers forbid passthrough: **X/Twitter absolutely** (Dev Agreement III.A(e) service-bureau ban plus III.G "will not make available to any third party any token, key, password": no exceptions; drop it from BYOK), and **Reddit by an explicit qualified ban** the first research pass missed (Developer Terms §1.4/§7.4, incorporated by the Data API Terms: "You may not share your Access Info with any other third party without Reddit's permission"). That leaves **YouTube** (defensible under its written agent-with-confidentiality-duty carve-out, which your published API terms must actually contain) and **Mastodon** (instance-scoped tokens exist precisely for third-party tools) as the clean BYOK providers.

---

## 2. The dataset reality

### Kaggle tweets: dead AND license-poisoned
| Dataset | Actual last update | License claim | Verdict |
|---|---|---|---|
| `aryansingh0909/elon-musk-tweets-updated-daily` | **2023-06-29** (metadata still says "daily") | CC0-1.0 (invalid: snscrape-collected) | Dead |
| Russia vs Ukraine Tweets (Daily Updated) | **2023-02-28** | "unknown" | Dead |
| Earthquakes Alerts / India's Political Parties (Daily Updated) | Collection stopped 2023; content removed (files now 22 bytes; removal metadata shows 2024-05-15) | n/a | Taken down |
| All Kaggle "tweets" datasets sorted by lastUpdated (checked 2026-08-09) | Nothing but static one-off uploads | n/a | No live tweet dataset exists |

Even if a fresh one appeared: the X Developer Agreement (live text, last updated 2026-04-27) bans training foundation/frontier models on X Content (III.A(k)) and redistribution/derivative works (III.A(d)); individual tweets are author-copyrighted and a scraper cannot relicense them. Mirroring to Kaggle/HF launders nothing. **Neither serving- nor training-eligible.**

### Kaggle Reddit: genuinely fresh, same old rights problem
`asaniczka/public-opinion-on-republicans-daily-updated` (2.48 GB), `...climate-change...` (519 MB), `...israel-palestine...` (1.55 GB) all updated **2026-08-09**, labeled ODC-By, an uploader claim over Reddit-user-copyrighted content that Reddit's terms don't permit relicensing. bwandowando's ~15 country-subreddit datasets (also updated 2026-08-09) carry Kaggle's license value literally reading `reddit-api`, Kaggle's own concession. Same-portfolio decay: asaniczka's Democrats dataset silently stopped 2025-11-18, Russia-Ukraine 2025-07-08. **Training-blocked** (the repo audit's uncurable Reddit problem); at most serving-only under the 10-min-cache posture, but daily whole-file re-downloads (~1.5 GB, no deltas) plus single-maintainer fragility make it not worth it. Skip.

### Hugging Face: the one real find, plus noise
- **`open-index/hacker-news`**: 49,156,875 items, 2006-10 through 2026-08-09 23:10 UTC, updated every 5 minutes (verified at the file-tree level, defeating the README-bump gotcha: `today/2026/08/09/` held genuine 5-minute parquet blocks). Delta-friendly layout: monthly parquets + 5-min live blocks, DuckDB-queryable. Declared ODC-By, but note the skeptic's correction: that label is itself uploader-declared over HN-user comment text. **Eligibility rests on your repo's own HN whitelist in docs/continuous-training-plan.md, not the dataset's label**. Caveats: unaffiliated community mirror; comment text is raw HTML; scores are point-in-time. **Training-eligible (as HN); serving redundant** (your hackernews connector already covers live).
- `AlastairH/bbc-news-logger` (updated 2026-08-09): live but license "other" and BBC copyright over article text. Skip. `shaurya03/tech-news-daily`: no license. Skip. HF tweet datasets: one-off scraped snapshots, no recurring feed.

### Kaggle platform terms [likely]
Free API access verified by use (CLI v2.2.1); ~10,000 calls/day is a forum figure. Kaggle's live ToS could **not** be fetched (reCAPTCHA-blocked on every route). If any Kaggle dataset ever becomes load-bearing, a human must read kaggle.com/terms in a browser first.

**Honest conclusion:** the dataset category yields exactly one durable win: the HN archive as training backfill. "Free, up-to-date dataset" for social text in 2026 is otherwise either stale, license-invalid, or both. The live-API connectors below are the real version of this idea.

---

## 3. New connectors worth building (ranked)

> Note: anything marked training-eligible below was a **proposed amendment** to the then-settled HN+RSS-only corpus policy in docs/continuous-training-plan.md. The sign-off has since happened: **docs/corpus-policy.md (2026-08-10)** adopted Bluesky (under four binding conditions), GDELT's own fields, and the HN backfill, held Lemmy at serving-only, and fixed a purge-and-retrain revocation rule. Per-source shipped status is noted inline and summarized in section 6.

### 1. Bluesky (highest value: the post-Twitter public-text source)
- **Auth:** keyless worked today via `api.bsky.app/xrpc/app.bsky.feed.searchPosts` but that host is undocumented for search; the documented unauth host `public.api.bsky.app` 403s searchPosts (mid-2026 restriction). Build with an optional **free-account session** (`com.atproto.server.createSession` + app password) and an unauth fallback that paginates by walking `until` = last `createdAt` with `sort=latest`, deduping on URI (unauth cursors 403 everywhere).
- **Limits:** 3,000 req/5 min/IP; 30 sessions/5 min.
- **Shape:** searchPosts is already Query-shaped (q, sort, since/until, lang, author, domain, tag, limit 1-100, cursor) with full `record.text` in JSON.
- **Serving:** yes, clean.
- **Training:** **the only realistic candidate to join HN+RSS.** ToS (2025-08-14) has no scraping/AI/ML clause; robots.txt on both hosts affirmatively allows crawling ("Crawling the public parts of the API is allowed") and points bulk users at the firehose; Bluesky PBC states it cannot and does not forbid third-party training. This is an absence-of-prohibition argument under your own HN standard, not an affirmative grant: posts stay user-copyrighted. **Mandatory conditions** (skeptic-upgraded from optional): harvest via the free Jetstream firehose (`wss://jetstream2.us-east.bsky.network/subscribe?wantedCollections=app.bsky.feed.post`, ~850 MB/day), persist the author DID with every corpus row, and gate Durable=true behind honoring the "user intents" opt-out framework (proposal 0008, unshipped as of 2026-08-09) the day it ships. Reputational caveat: the Nov-2024 HF 1M-post backlash.
- **Effort:** moderate: trivial HTTP search connector; the firehose harvester is a separate ingestion job.
- **Shipped:** both halves. The serving connector is in the registry, and `api/cmd/sentilyzer-harvest` (Bluesky is the default `-source`) streams Jetstream with language/length filters and deterministic sampling, persists the **author DID on every corpus row**, honors **delete events as tombstones** and account deactivations as account-scoped tombstones: the mandatory conditions above, as bound in docs/corpus-policy.md (the proposal-0008 opt-out condition stands, to be adopted the day it ships).

### 2. GDELT DOC 2.0 (cleanest license in the entire survey)
- **Auth:** none, no registration.
- **Limits:** **1 req/5 s per IP**, enforced with ~15-min blocks; the 429 body is prose, not JSON. Your gateway has one shared egress IP. Needs a **process-wide** limiter and exclusion from wide topic fan-outs.
- **Shape:** one HTTP GET (`query`, `mode=ArtList`, `format=json`, `maxrecords=250`, timespan/startdatetime, sort; TimelineTone/ToneChart for precomputed tone). Returns **headlines + URLs + metadata + tone, not article text**; 15-min freshness. Same-day results verified live.
- **Serving:** yes: "unlimited and unrestricted use for any academic, commercial, or governmental use of any kind without fee," redistribution allowed, citation required. The only affirmative grant surveyed.
- **Training:** GDELT's own records (headline/tone/URL) are storable under its grant; headlines carry thin third-party copyright (same posture as the RSS summaries the audit already accepts). Candidate to join the corpus, but expect domain shift (headlines are not social opinion); consider using GDELT's own tone as a separate signal instead of scoring headlines through the student model.
- **Effort:** trivial + the rate limiter.
- **Shipped:** both a serving connector and a batch harvester (`sentilyzer-harvest -source gdelt`). Harvest rows carry **GDELT's own fields only** (headline title, source domain, seen date), never linked article text, and a ticker enforces the 1 req/5s discipline on every request including the first. **Known limitation, documented in code:** the harvester's throttle and the serving connector's throttle are independent, so a harvest running while the API answers GDELT queries from the same IP can briefly reach 2 req/5s. Harvesting is an occasional batch task; pause it if GDELT starts returning 429s.

### 3. open-index/hacker-news ingestion (dataset, not a live connector)
- **Auth:** none (HF public dataset).
- **Use:** one-time bulk pull of monthly parquets = **20-year training backfill** of an already-whitelisted source; optionally poll the 5-min blocks, though the existing hackernews connector already covers live serving.
- **Serving:** redundant. **Training:** yes, as HN (rely on your audit, not the dataset's ODC-By label).
- **Effort:** small ingestion job (DuckDB/parquet) + staleness alert; no Connector interface work.
- **Shipped:** as the training pipeline's DEFAULT data source: the Modal app (`ml/sentilyzer_ml/pipeline/modal_app.py`) streams the archive's monthly parquets straight into a Modal Volume, incrementally by month, with `--from-month`/`--to-month` selecting the window.

### 4. Lemmy
- **Auth:** keyless (`GET /api/v3/search?q=...&type_=Comments&sort=New`; same-day comment verified on lemmy.world). Limits are per-instance (`rate_limit_search` in `/api/v3/site`).
- **Serving:** yes, clean; ship `Durable=false`.
- **Training:** defensible-by-your-own-standard on instances with no AI-crawler blocks (lemmy.world and lemmy.ml pass today, unlike Mastodon), but weaker than Bluesky and per-instance. Revisit only with automated per-instance robots.txt/ToS checks at connector-config time. Volume is modest.
- **Effort:** trivial (Reddit-shaped JSON) + instance allowlist.

**Tier-2 (only if more corpus breadth is wanted):** Stack Exchange (free key, ~10k req/day, content itself CC BY-SA: storage affirmatively licensed, but attribution columns required; procedural skew) and Nostr NIP-50 relays (keyless, contract-free, niche/crypto-skewed).

**Rejected, with reasons:** **PieFed** (robots.txt `Content-Signal: ai-train=no, ai-input=no`, a machine-readable no that arguably reaches even serving; skip entirely). **Guardian** (free tier is non-commercial-only and its commercial page literally enumerates "sentiment analysis where content is not reproduced" as a paid use case: do not wire the free key in). **NYT** (ToS bans content use in "the development or operation of a machine learning or artificial intelligence (AI) system": blocks serving, not just training). **NewsAPI-class aggregators** (24-hour delay, 100 req/day: demo tiers; GDELT dominates for free). **Farcaster** (topic search is a paid Neynar product; Neynar now owns the protocol) [likely]. **4chan** (no search endpoint + reputational). **Tumblr** (Automattic's AI-licensing pivot leaves Reddit-shaped rights murk) [likely].

---

## 4. BYOK design spec

**Providers:** YouTube + Mastodon (clean). Reddit: explicit qualified ban. Recommend dropping from v1 (see below). **X/Twitter: do not ship.** *(Shipped exactly so: YouTube + Mastodon only; Reddit and X were never wired. Section 6 records the delivered surface.)*

### Transport: request headers, edge only
```
X-Sentilyzer-Youtube-Api-Key:      <caller's YouTube Data API key>
X-Sentilyzer-Mastodon-Instance:    <https base URL of the caller's instance>
X-Sentilyzer-Mastodon-Token:       <caller's access token>
# only if Reddit BYOK is kept at all:
X-Sentilyzer-Reddit-Client-Id:     <caller's OAuth client_id>
X-Sentilyzer-Reddit-Client-Secret: <caller's client_secret>
```
Never in the URL (server logs capture URLs); never in the body. The headers-over-body choice rests on logging-pipeline practice and industry precedent (LiteLLM strips provider-auth headers by default; Portkey's stateless no-vault mode is header-based; access logs don't capture headers and collector header-redaction is mature, while body-capture silently swallows secrets). OWASP itself permits body **or** headers for POST but mandates TLS and bans URL-borne secrets (skeptic-corrected attribution).

### gRPC metadata (Go gateway → Python worker)
Identical names, lowercased: `x-sentilyzer-youtube-api-key`, `x-sentilyzer-mastodon-instance`, `x-sentilyzer-mastodon-token` (+ reddit pair if kept). Rules [likely, from gRPC docs]: lowercase is forced; charset a-z 0-9 `-` `_`; never a `grpc-` prefix; stay under the 8 KB default `max_metadata_size` (keys are <200 B, fine). The gateway's own `Authorization` header is **never** forwarded upstream: gateway auth and passthrough auth stay in separate channels (LiteLLM precedent).

### Lifecycle rules
1. **Parse at the edge** into a per-request credentials struct carried in request context; zero/drop when the fan-out completes. No vault, no encryption-at-rest problem: statelessness is the design (the OpenRouter/Nango stored-connection model is exactly what this avoids).
2. **Sole persistence exception:** Reddit app-only tokens (`grant_type=client_credentials` via HTTP Basic; 1-hour TTL, no refresh token) may be held in memory keyed by SHA-256(client_id|secret), TTL capped below expiry (moot if Reddit BYOK is dropped).
3. **Never log:** scrub the entire `X-Sentilyzer-*` namespace from access logs and all middleware; strip unrecognized provider-auth-shaped headers (`x-api-key`, `api-key`, ...) by default.
4. **Redact upstream auth errors:** provider 401/403 bodies can echo the key or client_id: return only "upstream auth failed for <connector>", never the raw body.
5. **Cache isolation** (RFC 9111's shared-cache rule applied to the 10-min in-memory cache): BYOK-fetched entries keyed `(connector, normalized query, SHA-256 of exact credential material)`. Caller X's response must never satisfy caller Y. Keyless connectors keep the shared cache. Never the raw key in a cache key.
6. **TLS-only:** reject BYOK headers arriving over plaintext.
7. **Fallback:** server-side keys, if configured, are used when no BYOK headers are present (the user's fixed direction).
8. **Mastodon SSRF guard:** the caller now controls an outbound fetch target: enforce https, resolve and block private/link-local ranges, consider regex/allowlist validation (LiteLLM's caller-supplied `api_base` precedent).

### Per-provider ToS constraints (live text, 2026-08-09)
- **X/Twitter: forbidden, drop from BYOK.** Dev Agreement III.A(e) bans service-bureau/managed-services provision and third-party credential links; III.G: "You will... not make available to any third party any token, key, password, or other login credentials to the X API." No agent exception. Every caller would breach, with Sentilyzer as the breach vector at scale. Keep X server-key-only or drop it.
- **Reddit: explicit qualified ban (skeptic correction; the earlier "no explicit ban / improves §2.8 compliance" claim is withdrawn).** The Data API Terms preamble incorporates the Developer Terms, whose §1.4 states: "You may not share your Access Info with any other third party without Reddit's permission" (Access Info = tokens, keys, passwords, login credentials; APIs in scope), reinforced by §7.4. Unlike X the ban is qualified ("without Reddit's permission"), so the cure path is the caller obtaining Reddit's blessing: realistically the §3.1 commercial agreement they needed anyway, since §3.2's ban on deriving revenue from provision of the Data API binds Sentilyzer whichever key is used. **Recommendation: drop Reddit from v1 BYOK**; if kept, the docs must state the caller needs Reddit's permission. Also: script apps only access the developer's own account.
- **YouTube: defensible gray zone, not a green light.** Developer Policies III.D.1.a permits sharing credentials with "agents operating solely on your behalf and under a written duty of confidentiality." Conditions: (a) your published API terms actually contain that written confidentiality duty, (b) the key serves only that caller's request (never pooled, never persisted), (c) quota and identity are honestly the caller's project. (The YouTube ToS does not incorporate the generic Google APIs ToS, so no stricter clause overrides this, skeptic-verified.) Training use of YouTube data stays banned regardless (30-day retention + derived-data ban).
- **Mastodon: the natural fit.** Federated, no central developer agreement; instance-scoped, non-expiring tokens exist precisely so third-party tools can act on a user's behalf. Two fields required (instance + token); SSRF validation per rule 8.

**Contract work is part of the feature:** publish the written confidentiality clause (load-bearing for YouTube's carve-out), the never-stored guarantee, and the Reddit commercial-use warning.

---

## 5. Honest limits: what stays impossible for free

- **Fresh tweets, at any price you'd call free.** Free X data ended in 2023; every mirror is stale, taken down, or both, and doubly ToS-blocked (training and redistribution). X BYOK is also contract-forbidden. Bluesky's firehose is the real version of what the Kaggle-tweets idea was reaching for.
- **Full article text of major news.** Guardian prices sentiment analysis as a commercial use; NYT bans ML-system operation outright (serving included); free aggregator tiers are 24-hour-delayed demos; GDELT grants only its own metadata/tone: following its URLs and storing article text reintroduces each publisher's copyright.
- **Reddit at scale without Reddit's blessing.** Server-key commercial use needs a §3.1 agreement; BYOK needs Reddit's permission (§1.4); Kaggle mirrors are license-invalid and single-maintainer-fragile. There is no free clean path.
- **Affirmative training rights to opinion-rich social text.** Only Stack Exchange/Wikimedia content is affirmatively licensed (CC BY-SA, with attribution + ShareAlike duties); Bluesky, GDELT headlines, and Lemmy are absence-of-prohibition judgments under your own HN standard: defensible, but revocable by a robots.txt edit or a shipped opt-out framework. The sign-off amending the HN+RSS-only policy happened 2026-08-10 (docs/corpus-policy.md), paired with a purge-and-retrain revocation rule for exactly that risk.
- **Operational overhead is not optional.** Any dataset source needs an ingestion cron + local store + staleness alerting (daily datasets die silently; takedowns to 22 bytes are real); GDELT needs a process-wide rate limiter behind your single egress IP; ToS drift is the norm this year: re-run robots.txt/ToS checks at connector startup or in CI, not annually. [likely] Kaggle's own ToS remains human-unverified behind reCAPTCHA.

**Net effect now that the ranked plan is adopted (2026-08-10 sign-off):** serving gained Bluesky + GDELT keyless connectors (Lemmy stays unbuilt, held serving-only pending per-instance checks); BYOK shipped for YouTube + Mastodon; the training corpus expanded from HN+RSS (~10k docs/day) to HN(+20-year backfill)+RSS+Bluesky+GDELT-headlines, roughly two orders of magnitude more eligible volume, with Bluesky the only genuinely opinion-rich addition.

---

## 6. What shipped (verified in-repo 2026-08-11)

The survey above stops at recommendations; this section is the delivery record, verified by reading the code.

### The harvester: one binary, three sources

`api/cmd/sentilyzer-harvest` writes corpus-eligible sources into the training corpus, selected by a `-source` flag, and stays a separate process from the serving gateway on purpose (the gateway persists nothing; the harvester persists rows only for sources docs/corpus-policy.md admits):

- **`-source bluesky` (the default, streaming):** Jetstream firehose with language/length filters and deterministic sampling; author DID on every row; delete events become tombstones, account deactivations become account-scoped tombstones. Runs until signalled or `-duration` elapses, with a cursor file for resumption.
- **`-source rss` (batch):** one pass over a curated feed list (overridable with `-feeds`), then exit. Rows carry **summary-level content only**: the feed's own title + summary, never the linked article body, HTML-stripped and normalized identically to the serving connector so the same item content-hashes the same on both paths. A failing feed logs and continues; a failing write aborts, because silently dropped rows are worse than a loud failure.
- **`-source gdelt` (batch):** one throttled pass over broad default queries (overridable with `-queries`, lookback via `-timespan`), then exit. Rows carry **GDELT's own fields only** (headline title, source domain, seen date); the linked article is never fetched or stored, because that would reintroduce each publisher's copyright. A ticker gates every request, including the first, to 1 req/5s. **Known limitation:** this throttle is independent of the serving connector's, so a simultaneous harvest and serving load from one IP can briefly reach 2 req/5s; harvesting is an occasional batch task, so pause it if 429s appear.

### The serving-side location filter

The serving API gained a best-effort `location=` parameter, and the docs are deliberate that it is **keyword-level narrowing, not geo-tagging**: "barbershops" with `location=Austin` matches posts mentioning both words, not businesses located there.

- On every keyword-search platform (hackernews, bluesky, mastodon, reddit, twitter, youtube), a non-empty location is ANDed into the platform query as a quoted phrase; the platform's own search decides what the phrase means.
- On **GDELT**, a location that names a country (English name or two-letter code, case-insensitive) is translated to GDELT's `sourcecountry:` operator instead (GDELT's country coding is FIPS-based; the gateway owns the translation table). Anything else (cities, regions, typos) falls back to the quoted phrase.
- On **RSS** there is no upstream search, so the filter degrades to a local substring match over the same title+description haystack the topic matches against. Connectors without a keyword query (stocktwits, mock) ignore it, and responses carry no location fields.

### BYOK, as specced in section 4

Shipped for **YouTube + Mastodon only**: `X-Sentilyzer-Youtube-Api-Key`, `X-Sentilyzer-Mastodon-Token`, plus an optional `X-Sentilyzer-Mastodon-Instance` override, mirrored as lowercased gRPC metadata. The lifecycle contract landed as written: credentials live in memory for one request, never logged, never persisted, never echoed into errors, never pooled across callers; the only value that outlives the request is a one-way fingerprint that partitions the cache per credential set; caller-supplied Mastodon instances must be public https hosts (private and link-local targets are refused, the SSRF guard of rule 8). Server-side keys remain the fallback when no BYOK headers arrive. Reddit and X/Twitter were dropped exactly as recommended, with the refusal rationale (X Dev Agreement III.G, Reddit Developer Terms 1.4) documented in the code and the README.