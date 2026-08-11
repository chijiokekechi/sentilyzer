<!--
Generated 2026-07-20 by a multi-agent research pass, verified against live sources.
The chosen deployment: SERVE on one free Oracle Cloud Always-Free A1 box; TRAIN on Modal
(training-only, on the recurring $30/mo Starter credit). Supersedes the two-box HA sketch and
the paid Modal/Fly design in continuous-training-plan.md for THIS user's chosen setup.

Verified-good facts that decide the plan:
  - Modal's $30/mo Starter credit is RECURRING monthly (not a one-time signup bonus), so
    WEEKLY training is $0 out of pocket indefinitely; DAILY is ~$2.86/mo. Re-verify the
    pricing wording periodically -- "free forever" is contingent on it staying recurring.
  - The 12 GB Oracle box fits the torch teachers, so serve teachers NOW (Path A, zero new
    build); add the Modal-distilled ONNX student LATER (Path B) as a deliberate Stage 2.

Honest ceiling: a single free box is best-effort (~99%-ish, unmeasured), auto-restarts crashed
CONTAINERS and pages you off-box in minutes -- but a host crash/OOM/Oracle-maintenance is down
until the host recovers or you SSH in. This is NOT "always available"; never advertise it as such.
Prices/limits/credit-terms drift -- re-verify before relying on them. Not legal advice.
-->

> **ARCHIVED (2026-08-10):** the project settled on **on-demand end-to-end
> runs only**: train on your own Modal account with one command, serve
> locally when needed. No always-on box, no R2, no promotion pointer. This
> document describes the always-on operated-API design that was NOT chosen;
> it is kept for reference only.

# Sentilyzer on one free Oracle A1 box + Modal training: the plan

## 1. The answer, in five sentences

Run the existing `sentilyzerd` + `sentilyzer-ml` compose stack behind a containerized Caddy (automatic Let's Encrypt TLS, `h2c` for gRPC) on a single Oracle Cloud Always-Free A1 box (arm64, 2 OCPU / 12 GB / 200 GB, ~$0 infrastructure + ~$10/yr for a domain), upgraded to Pay-As-You-Go to dodge idle-reclamation, with `restart: unless-stopped` + an autoheal sidecar + real healthchecks, an **off-box** Better Stack monitor, and a 1-minute keep-warm cron. This is a **best-effort single box**: usually up (~99%-ish, unmeasured), auto-restarts *crashed containers*, and pages you off-box within minutes, but it is **not** highly available: a host crash, OOM, or Oracle maintenance takes the whole API down until the host recovers or you SSH in (minutes to hours), and a bad instance termination can be unrecreatable if Arm capacity is gone. Serve the **frozen teachers directly now** (Path A): it ships today with zero new build, the common request runs a single RoBERTa at ~65-240 ms/doc `[likely]`, and both teachers fit 12 GB with ~6 GB headroom. Then add the Modal-distilled INT8 ONNX student (Path B) as a deliberate Stage 2. Modal-for-training-only is genuinely near-$0 because the **$30/mo Starter credit is recurring every month** (not a one-time signup bonus) `[verified 2026-07-20]`, so **weekly** training (~$7/mo gross) is **$0 out of pocket indefinitely** and **daily** is **~$2.86/mo** (not the design doc's stale $13.12), the only catch being that "free forever" depends on Modal keeping the credit recurring. The student is the *right* serving artifact for this small box (≈3-4× faster, ~1 GB RAM) and gives you the learning system you want, but it is strictly *slightly less accurate* than the teacher (frozen Dec-2021 ceiling) and is a multi-day greenfield build (no onnxruntime backend, no pipeline exist yet), so teachers-first isn't just easier: it's a prerequisite, since the student needs a teacher-labeled corpus that does not exist on day one.

---

## 2. The serving-model call: teachers now (A), student deliberately next (B)

**Do A now, build B as Stage 2. They are not mutually exclusive, and the order is forced.**

| | **Path A: serve frozen teachers** | **Path B: serve Modal-distilled INT8 ONNX student** |
|---|---|---|
| Exists in repo? | **Yes**: `TransformerBackend` in `ml/sentilyzer_ml/inference.py` | **No**. Greenfield: no `onnxruntime`, no `[serve]` extra, no ONNX artifact, no harvest/label/distill/eval/export/deliver/hot-swap pipeline |
| Hot-path model | **Single** RoBERTa-base (`cardiffnlp/...`); DeBERTa loads **only** for aspect requests `[verified inference.py]` | 6-layer INT8 distilroberta, single two-head graph |
| Latency (2 Ampere N1 cores) | ~65-240 ms/doc short text; ~200-400 ms at 128 tokens `[likely]`; 60-doc request ≈ 4-15 s warm | ~17 ms/batch warm `[likely]`; 60-doc request ≈ 1-4 s |
| RAM | ~2-2.5 GB resident, ~4.3 GB peak `[verified]` | ~0.5-1 GB, **no torch** |
| Accuracy | Teacher ceiling ~72-74 macro-F1, **Dec-2021 cutoff** `[verified]` | **Strictly ≤ teacher** (domain gap + ~1.8% distillation + ~0.4 pt INT8) `[verified]` |
| Ops burden | Low; resident teacher keeps memory >20% so box self-protects from reclamation | **Adds** a mandatory keep-warm cron (student RSS can sit *below* the 20% memory floor) |
| $ | $0 infra | $0 infra + Modal training (§4) |

**Why A must come first (not just "is easier"):** Path B still needs the **frozen teachers to generate the soft labels** it distills from, and on day one there is **no accumulated teacher-labeled corpus**, so a first student would be distilled on nothing and underperform the teacher it approximates. You must run the teachers first, accumulate a corpus, *then* distill. Serving those same loaded teachers to users is near-zero marginal work. `[verified]`

**The honest caveat, stated plainly:** on *this* box the student's wins are **latency, RAM footprint, keep-warm cost, and the continuous-learning architecture**, **not** higher accuracy. The speed/RAM wins only *bind* under sustained concurrency the box does not have yet. If what you actually want is better 2026-language quality, the only real lever is a **newer teacher**, not a faster cron. `[verified]`

**Add the student (flip to Path B) when any of these is true:**
1. Real traffic makes the ≈3-4× latency/RAM win pay for itself (e.g. p99 under load breaches your SLA, or teacher RAM + Caddy + Kuma starts flirting with OOM), **or**
2. You want the flywheel/learning-system for its own sake (you've said you do), **or**
3. You want onnxruntime-only serving to drop torch's ~2.5 GB off the 12 GB box entirely.

---

## 3. Oracle A1 setup runbook (copy-pasteable, ordered)

Target: Ubuntu 24.04 (`Canonical-Ubuntu-24.04-aarch64`), `VM.Standard.A1.Flex`, **2 OCPU / 12 GB**, 200 GB block. All numbers `[verified 2026-07-20]` unless marked.

### Step 0: Verify the free envelope before you build
Free A1 is now **2 OCPU / 12 GB / 200 GB**, halved from 4/24 in June 2026 with no announcement and inconsistent enforcement. **Design for 12 GB.** Practical RAM budget with the full Path-A stack: teachers ~4.3 GB peak + gateway ~30-100 MB + Caddy ~30 MB + autoheal ~10 MB + Uptime-Kuma ~120 MB + OS ~0.4-1 GB ≈ **~3.2 GB idle / ~5-6 GB peak** → fits with ~6 GB headroom. `[verified]`

### Step 1: Upgrade to PAYG *first*, then launch (beats "Out of capacity")
```
# OCI Console → Billing → Upgrade to Pay As You Go → add a card.
# You are billed $0 as long as usage stays within 2 OCPU / 12 GB / 200 GB.
# IMMEDIATELY: Billing → Budgets → create a $1 budget alert.  <-- catches any accidental overage
```
PAYG gives capacity priority **and** exempts the box from idle-reclamation. `[verified/inference]` Then create the instance (Ubuntu 24.04 aarch64, A1.Flex 2/12, paste SSH **public** key, assign a public IPv4). If you hit **Out of capacity**, don't hand-retry: automate across every availability domain in your home region:
```
# hitrov/oci-arm-host-capacity (PHP; archived Aug-2024 but reported working). Needs an OCI API key.
* * * * * /usr/bin/php /path/to/oci-arm-host-capacity/index.php >> /var/log/oci.log 2>&1
```
The `A2.Flex → oci compute instance update --shape VM.Standard.A1.Flex` capacity trick is reported (May 2026) but **unverified, last resort only**. `[flagged]`

### Step 2 (THE #1 TRAP): open 80/443 in BOTH layers
Opening the port in the OCI VCN does **nothing** until you also insert an iptables ACCEPT *above* Ubuntu's default REJECT.

**Layer 1 (cloud):** OCI Console → Networking → VCN → Security Lists (or NSG) → Add Ingress: `Source 0.0.0.0/0, TCP, Dest 80`; repeat for `443`. Keep **22 scoped to your home IP**, not `0.0.0.0/0`.

**Layer 2 (OS):** Ubuntu's OCI image has a REJECT before the end of the INPUT chain, so *appending* is silently ignored. You must **insert**:
```
sudo iptables -I INPUT 6 -m state --state NEW -p tcp --dport 80  -j ACCEPT
sudo iptables -I INPUT 6 -m state --state NEW -p tcp --dport 443 -j ACCEPT
sudo netfilter-persistent save
sudo iptables -L INPUT --line-numbers    # CONFIRM the ACCEPTs sit ABOVE the REJECT
```
The insert position `6` can vary by image revision. Always verify with `-L --line-numbers`. `[flagged]` Simpler alternative: `sudo apt-get -y purge netfilter-persistent iptables-persistent` (then only the VCN firewall applies).

### Step 3: Harden the host
```
# /etc/ssh/sshd_config.d/60-hardening.conf
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
```
```
sudo systemctl restart ssh
sudo apt-get update && sudo apt-get -y install fail2ban unattended-upgrades
sudo dpkg-reconfigure -plow unattended-upgrades   # enable auto security updates
sudo systemctl enable --now fail2ban              # sshd jail is on by default in 24.04
```
Your firewall of record is the VCN (only 22/80/443 ingress).

### Step 4: Docker + Compose (native arm64)
```
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker $USER    # then re-login
docker compose version           # v2 plugin included
docker info --format '{{.Architecture}}'   # -> aarch64
```
**Ampere thread cap:** on only 2 OCPU, set `OMP_NUM_THREADS=2` for the worker (and `torch.set_num_threads(2)`), else torch oversubscribes and p99 suffers. For the future ONNX student, set `intra_op_num_threads=2` (ORT ignores cgroup quotas). `[verified]` Never `pip install onnxruntime-gpu` or a CUDA torch index here: no aarch64 wheels. Pin torch CPU: `--index-url https://download.pytorch.org/whl/cpu`.

### Step 5: Domain + Caddy + TLS
Buy one **Cloudflare Registrar .com (~$10.11/yr, no renewal markup, free WHOIS privacy)** `[verified]`. Point **`api.<domain>` and `grpc.<domain>` A-records at the box's public IPv4 before first boot** so ACME HTTP-01 succeeds. (Free alt: a `*.duckdns.org` subdomain. Skipping the domain forces `tls internal` self-signed certs → every API caller gets trust errors → defeats a public callable API. Spend the $10.) Don't bother with Cloudflare's orange-cloud proxy for one origin: it adds a second cert, a hop, and doesn't natively proxy gRPC. Caddy + LE wins.

`Caddyfile` (Caddy runs *inside* the compose stack, reaching app containers by service name):
```
api.example.com {
    reverse_proxy api:8080
}
grpc.example.com {
    reverse_proxy h2c://api:9090
}
```
The `h2c://` is load-bearing: grpc-go serves cleartext HTTP/2 on :9090, so Caddy must speak h2c upstream while terminating real TLS at the edge. Port 80 must stay open for ACME on **both** hostnames.

### Step 6: Self-healing (autoheal + real healthchecks)
`restart: unless-stopped` handles crashes; the `willfarrell/autoheal` sidecar restarts containers Docker marks **UNHEALTHY**, so the healthchecks must be real (see §3b for the two repo fixes this requires). Autoheal only restarts a container on a *live* host; it does nothing for a dead host (§6).

### Step 7: Monitoring/alerting (run BOTH)
- **Off-box, authoritative:** Better Stack free (10 monitors + 10 heartbeats + 1 status page, 3-min checks) `[verified]`. Point one HTTPS monitor at `https://api.<domain>/health`; wire free push to Telegram/Ntfy/email. **This fires even when the whole box is down**, the reason it must be off-box.
- **On-box, granular:** Uptime-Kuma (`louislam/uptime-kuma`, ~120 MB) for per-container Docker health, gRPC/TCP :9090, the ML push probe, and a public status page. It dies with the box (fine, because Better Stack covers that case).

### Step 8: Keep-warm
cron-job.org free allows **1-minute** jobs (60 exec/hr) `[verified]`. POST a tiny fixed payload to `https://api.<domain>/v1/analyze/text` every 1-5 min to keep the model resident (callers never eat the cold load) and pin CPU/network above the reclamation floor. Point a Better Stack heartbeat at the same job so a *stalled* cron also alerts. With PAYG this is latency insurance, not survival. `[verified]`

---

## 3b. What to change in the repo to deploy

**Verified-good news (corrects the briefing):** the client **already** chunks to `maxBatch` and length-orders to tame padding: `Classify` in `api/internal/inference/client.go:129` uses `lengthOrder` + `chunkRanges` from `api/internal/inference/batch.go`, which documents the ~36× padding fix. So the briefing's "the default 60-doc request fails today / fix the 32-cap first" is **stale for this snapshot**: it's done. Client and worker both default `SENTILYZER_ML_MAX_BATCH=32`; keep them equal.

| # | Change | File(s) | Status |
|---|---|---|---|
| 1 | Add **`/livez`** (gateway-only 200, no ML dep) for the container HEALTHCHECK; keep existing **`/health`** (ML-aware, calls `Service.Health`) for the LB/Kuma/Better-Stack probe. Optional `/readyz` = ML-aware alias. Register `/livez` **outside** the `negotiateMedia` middleware (or make it Accept-tolerant) so a plain GET returns 200. | `api/internal/server/rest.go` (routes at :48) | **NEW**: today only `/health` exists |
| 2 | Add a **`-healthcheck`** subcommand: parse `flag`, GET `http://127.0.0.1:8080/livez`, exit 0/1. `main.go` has **zero flag parsing** today. | `api/cmd/sentilyzerd/main.go` | **NEW** |
| 3 | Add `HEALTHCHECK ... CMD ["/usr/local/bin/sentilyzerd","-healthcheck"]` (distroless has **no shell/curl**: a `curl` healthcheck fails forever and autoheal restart-loops the container). | `deploy/docker/api.Dockerfile` | **NEW** (Dockerfile is otherwise correct: CGO-free distroless static) |
| 4 | Replace the **no-op** ML healthcheck `["CMD","python","-c","import grpc; grpc.insecure_channel('localhost:50051')"]` (a lazy channel **always succeeds**) with a real probe. | `deploy/compose/docker-compose.yml:16` | **FIX** |
| 5 | New **single-box compose**: add `restart: unless-stopped` to every service; add `autoheal` sidecar + `labels: [autoheal=true]` on api/ml; add `caddy:2` (ports `80:80`,`443:443`, named `caddy_data` volume); **stop publishing** api/ml ports to `0.0.0.0` (Caddy reaches them over the compose network); set `SENTILYZER_USE_MOCK=false`; add `OMP_NUM_THREADS=2`; set ml healthcheck `start_period: 120s` for cold torch load. | new `deploy/compose/docker-compose.oracle.yml` | **NEW** (derive from existing) |
| 6 | The `Caddyfile` from §5. | new `deploy/compose/Caddyfile` | **NEW** |
| 7 | **Rate limiting** (briefing confirms none exists): add `github.com/go-chi/httprate` middleware in `Router()`: you already run chi v5.2.5. gRPC needs a separate `tap`/interceptor limiter. | `api/internal/server/rest.go` | **NEW** |
| 8 | **Should-fix, both paths:** `transformers>=4.45` is unbounded and resolves to 5.x, whose `from_pretrained` dtype default is `"auto"` → teachers can silently load fp16/bf16 (wrong/slow on CPU, corrupts soft labels for Path B). Pin `transformers>=4.45,<5` and pass `dtype=torch.float32` explicitly in the teacher loaders. | `ml/pyproject.toml:18`, `ml/sentilyzer_ml/inference.py` (`_load_general` :136, `_load_aspect` :153) | **FIX** |

**ML healthcheck replacement (item 4)**: minimum viable (real ready connection):
```yaml
healthcheck:
  test: ["CMD","python","-c","import grpc,sys; ch=grpc.insecure_channel('localhost:50051')\ntry:\n grpc.channel_ready_future(ch).result(timeout=3)\nexcept Exception: sys.exit(1)"]
  interval: 15s
  timeout: 5s
  retries: 3
  start_period: 120s
```
Better: implement `grpc.health.v1.Health` in `ml/sentilyzer_ml/server.py` (`serve()` at :105) and probe `Check()`, or bake `grpc_health_probe` into `deploy/docker/ml.Dockerfile`.

**Path-B-only repo work (deferred to Stage 2, §5):** new `ml/sentilyzer_ml/onnx_backend.py` (`OnnxBackend`) + `ml/sentilyzer_ml/model_store.py` (`ModelManager`); extend `ml/sentilyzer_ml/config.py` (frozen `Config` dataclass) with backend mode + R2 creds + poll interval; wire `server.py` (`InferenceServicer` reads `self.manager.current()` once per request; `Ready()` at :89 reports `served_run_id`); add `[serve]` extra (`onnxruntime>=1.27,<2`, `boto3>=1.40`, `tokenizers>=0.20`) and bump `requires-python` from `>=3.10` to `>=3.11` in `pyproject.toml`.

---

## 4. The Modal training plan (Stage 2, only for Path B)

Modal is **training-only** here: serving is on Oracle, so there is **no** `min_containers=1` warm container. Use **scale-to-zero** (set no `min_containers`): idle cost is $0. `[verified]`

### Cost (all `[verified 2026-07-20]`, rates unchanged from mid-2026)
- **$30/mo Starter credit is RECURRING monthly** (resets, no rollover, no card required). This is the decisive fact: it makes weekly training free *every month, indefinitely*, not free-for-N-months.
- Trainer pinned at **$0.99284/run** by the 45-min (2700 s) wall-clock cap on A10 (A10 $0.000306/s + 2 cores + 16 GiB).
- **Weekly cadence:** trainer $4.30/mo + pipeline ~$2.68/mo (label/prep/harvest/orchestrator) ≈ **$6.98/mo gross → fully inside the $30 credit → $0 out of pocket, forever.**
- **Daily cadence:** trainer $30.18/mo + $2.68 ≈ **$32.86/mo gross → ~$2.86/mo out of pocket.** **Not $0.** **Not** the design doc's stale **$13.12**: that figure wrongly included a $10.26/mo Modal warm-serving container this architecture deletes.
- Teacher-labeling on Modal T4 ≈ **$1.11/mo**, absorbed by the credit.
- **Flag:** "free forever" is contingent on Modal keeping the credit *recurring*. It is recurring today; if it ever reverts to a one-time signup credit, weekly training becomes ~$7/mo after month one. Re-check the pricing-page wording periodically. `[flagged]`

**Recommendation: ship weekly first (genuinely $0 forever); go daily only if you want the nightly loop and accept ~$2.86/mo.**

### Labeling: keep it on Modal T4, not Oracle CPU
Even though Oracle CPU is $0 cash, keep teacher-labeling on Modal's T4 (~$1.11/mo, absorbed by credit). Moving it to Oracle re-introduces ~2.5 GB of torch onto the 12 GB serving box, pegs both cores for ~15-20 min/day, contends with serving latency, and defeats the whole point of an onnxruntime-only student box. `[likely]` Only move it to Oracle if you want Modal's footprint at literal zero.

### Pipeline (harvest → label → distill → eval → promote)
1. **Harvest**: HackerNews + RSS **only** (the ToS gate; nothing else may enter the training corpus). `[verified]`
2. **Teacher-label**: frozen teachers (T4) emit soft labels; **pin `transformers<5` + `dtype=float32`** or labels corrupt.
3. **Distill**: INT8 ONNX student (~80 MB), 45-min wall-clock cap, **collapse firewall: re-init the student from the FROZEN teacher every run.** Checkpoint-and-resume (GPU functions are preemptible; `nonpreemptible` is unsupported for GPU).
4. **Eval gate (all must pass, on Modal, before the pointer is written):** T1 data sanity (`n_new≥2000`, `max_platform_share≤0.95`); T2 fidelity on a frozen held-out slice with anti-ratchet floors; T3 golden-HN macro-F1 ≥ baseline−0.02 (the real bar; TweetEval as drift tripwire only); T4 per-class recall floors `≥0.45` (guards neutral-class collapse); T5 serving budget (`onnx_bytes≤100 MB`, p99 latency). Keep `AUTO_PROMOTE=0` (manual) for 2-4 weeks. `[verified]`
5. **Promote**: write `students/current.json`. **Rollback** = re-write it with a prior `run_id`. Drill it: assert the live Oracle endpoint reports the rolled-back `run_id` within one poll cycle.

### Current Modal API essentials (SDK v1.5.x, `[verified]`)
`modal.Stub` is **removed** (raises `AttributeError`); use `modal.App`. Automounting is gone: add code explicitly via `add_local_python_source`. Renames: `keep_warm`→`min_containers`, `container_idle_timeout`→`scaledown_window`, `concurrency_limit`→`max_containers`, `web_endpoint`→`fastapi_endpoint`. Use `gpu="A10"` strings.
```python
app = modal.App("sentilyzer-train")
vol = modal.Volume.from_name("weights", create_if_missing=True)
@app.function(gpu="A10", timeout=2700,
              schedule=modal.Cron("30 5 * * 0", timezone="UTC"),   # weekly
              volumes={"/weights": vol},
              secrets=[modal.Secret.from_name("r2")])
def train(): ...
# training-only: do NOT set min_containers (scale-to-zero).  `modal deploy`
```

**Prerequisite:** none of this ships a user-visible byte until the **ONNX student + `OnnxBackend`** exist (§3b, §5). Build those first.

---

## 5. The model handoff: Modal → R2 → Oracle poll + hash + hot-swap

**Store recommendation: Cloudflare R2.** Modal Volumes have **no external plain-HTTPS pull path** (SDK/CLI only) `[verified]`, so an external store is mandatory. R2 free tier = **10 GB storage, 1M Class-A, 10M Class-B ops/mo, ZERO egress forever** `[verified]`. Poll math: `current.json` GET every 5 min ≈ 8,640 reads/mo = 0.09% of the free 10M; an 80 MB download happens only on promotion. Zero egress specifically dodges Oracle PAYG bandwidth cost. R2 is already the plan's delivery store (corpus + labels + students via boto3), so it adds **no new infrastructure**. Runner-up: HF Hub public repo (simplest no-credential pull, but splits delivery off, needs git-LFS + write token, publishes weights). B2 and GitHub Releases are dominated (metered egress; tag churn / non-atomic replace).

**Handoff shape:** Modal writes `students/<run_id>/{model.int8.onnx,tokenizer.json}`, then **last** writes `students/current.json` (single writer, last-write-wins). Oracle's Python worker runs a **background poll thread** that: GETs `current.json` → if `run_id` changed, downloads each file → **sha256-verifies against the pointer** → builds a fresh `InferenceSession` → **smoke-tests 2 canned inputs** → **only then** atomically rebinds `self._backend = candidate`. On any failure it logs and keeps serving the current model. The swap lives in **Python, not Go**: the atomic attribute store under the GIL sidesteps the use-after-free the Go in-process CGO swap would hit. `[verified]`

Pointer schema `r2://sentilyzer/students/current.json`:
```json
{ "run_id": "train:2026-07-15", "promoted_at": "2026-07-15T06:20:11Z",
  "files": { "model.int8.onnx": {"name":"model.int8.onnx","sha256":"9f2c…","bytes":79923184},
             "tokenizer.json":  {"name":"tokenizer.json","sha256":"4a8e…","bytes":2233110} },
  "metrics": { "golden_hn_macro_f1":0.71, "agree_doc":0.95, "recall_neutral":0.52 } }
```

`ml/sentilyzer_ml/model_store.py` sketch (the poll/verify/swap core):
```python
def poll_once(self):
    raw = self._s3.get_object(Bucket=self._bucket, Key=f'{self._prefix}/current.json')['Body'].read()
    ptr = json.loads(raw)
    if ptr['run_id'] == self._current_run_id: return          # cheap ~200B Class-B GET
    mp = self._fetch_verified(ptr, ptr['files']['model.int8.onnx'])
    tp = self._fetch_verified(ptr, ptr['files']['tokenizer.json'])
    cand = OnnxBackend(mp, tp, run_id=ptr['run_id'])          # BUILD before swap
    _smoke(cand)                                              # canned inputs; raises on garbage
    self._backend = cand                                     # atomic rebind; in-flight reqs keep old ref
    self._current_run_id = ptr['run_id']

def _fetch_verified(self, ptr, meta):
    dest = os.path.join(self._cache, ptr['run_id'], meta['name'])
    if os.path.exists(dest) and _sha256(dest) == meta['sha256']: return dest
    tmp = dest + '.part'
    self._s3.download_file(self._bucket, f"{self._prefix}/{ptr['run_id']}/{meta['name']}", tmp)
    if _sha256(tmp) != meta['sha256']:
        os.remove(tmp); raise ValueError('sha256 mismatch')   # truncated 80MB never touches ORT
    os.replace(tmp, dest); return dest                        # atomic rename only after hash passes
```
- **Servicer contract:** read `backend = self.manager.current()` **once** at the top of `Classify`/`ClassifyAspects` and use that local for the whole request. The poller does one atomic `self._backend = cand`. Never re-read mid-request.
- **Failure isolation:** wrap `poll_once()` in a catch-all with jittered backoff: a transient R2 outage or malformed pointer must **never** take down serving.
- **Creds (Oracle `.env`):** R2 **"Object Read only"**, bucket-scoped token (S3 API, GET/HEAD only: a compromised box can't corrupt the corpus or promote). `boto3` config: `endpoint_url=https://<ACCT>.r2.cloudflarestorage.com`, `signature_version='s3v4'`, `region_name='auto'`. Serving path needs **onnxruntime + tokenizers + numpy + boto3, no torch**.
- **Staleness alarm:** page when `served_run_id != current.json.run_id` for >10 min (worker exposes `served_run_id` via `Ready()`).

**Prerequisite:** `OnnxBackend` (the ONNX serving path) **does not exist yet**: today `ml/sentilyzer_ml/` has only the heuristic stub and `TransformerBackend`. Also verify the single-graph two-head ONNX export cleanly (plan open-Q #11); fallback is two ONNX files with a duplicated encoder (poller logic identical, `current.json` lists 3 files). `[flagged]`

---

## 6. Honest limits + true cost

**Where this is NOT "always available":**
- **One box = one point of failure.** `autoheal` + `restart: unless-stopped` are Docker-level: they recover a **crashed container on a live host**. They do **nothing** for a dead/rebooting/kernel-panicked **host**, an OOM that kills the box, host hardware failure, or Oracle maintenance (non-live-migratable instances get a reboot migration = "a short downtime"; **free tier is not eligible for Oracle Support**). A 3am host crash is down until Oracle's host recovers or you SSH in: **minutes to hours**. This is exactly why the Better Stack monitor must be **off-box** (on-box Kuma dies with the box). `[verified]`
- **"Out of capacity" is also a recovery risk, not just a create-time one.** A terminated or stop-to-resize instance may be **unrecreatable** if Arm capacity is gone, and post-June-2026 you can be locked to 2/12. Treat any termination as potentially **permanent downtime**. `[verified]`
- **PAYG reclamation-exemption is established practice, not a single quoted Oracle guarantee**, and PAYG overage-billing messaging is self-contradictory. Keep the **$1 budget alert** and the **keep-warm cron** as belt-and-suspenders. `[verified/inference]`
- **Realistic availability: ~99%-ish (unmeasured), with rare multi-minute-to-hour outages on host events.** Never advertise "always available."
- **torch teachers run "acceptably" only** warm, short docs, single RoBERTa, low concurrency (~65-240 ms/doc; 60-doc request ≈ 4-15 s warm). They roughly double for 128-token docs and add the second DeBERTa teacher for aspect requests, and degrade fast under real concurrency on 2 Neoverse-N1 cores (no BF16). `[likely]`

**True cost:**
| Item | Cost |
|---|---|
| Oracle A1 box (within free limits, PAYG) | **$0** `[verified]` |
| Domain (Cloudflare .com) | **~$10.11/yr** `[verified]` |
| Better Stack / Uptime-Kuma / cron-job.org / R2 | **$0** (free tiers) `[verified]` |
| Modal training: **weekly** | **$0/mo** (inside recurring $30 credit) `[verified]` |
| Modal training: **daily** | **~$2.86/mo** `[verified]` |
| **Total (Path A, weekly-B optional)** | **≈ $10/yr** |

**Biggest risk & the cap:** an **accidental PAYG bill** (June-2026 cut messaging contradicts itself on whether it bills PAYG for 4/24 overage). **Cap it** with the **$1 budget alert** (Step 1) and by never exceeding 2 OCPU / 12 GB. Secondary risk: **the "free forever" Modal claim** collapses if Modal makes the credit one-time. Re-verify the pricing wording before relying on it long-term.

---

## 7. Recommended order of operations

**Stage 0, today (provision & secure):** PAYG upgrade + $1 budget alert → launch A1 2/12 (retry-loop if "Out of capacity") → open 80/443 in **both** VCN and iptables (verify ACCEPT above REJECT) → harden SSH/fail2ban/unattended-upgrades → install Docker.

**Stage 1, this week (ship Path A, the live GA API):**
1. Buy the Cloudflare .com; point `api.<domain>` + `grpc.<domain>` at the IP.
2. Land the **repo changes in §3b items 1-8**: `/livez` route, `sentilyzerd -healthcheck`, api Dockerfile HEALTHCHECK, fix the ML healthcheck no-op, the single-box compose (autoheal + `restart: unless-stopped` + `USE_MOCK=false` + `OMP_NUM_THREADS=2` + service-name-only ports), the `Caddyfile`, `httprate` rate limiting, and the `transformers<5`/`dtype=float32` pin.
3. `docker compose -f deploy/compose/docker-compose.oracle.yml up -d` behind Caddy (LE TLS, h2c gRPC).
4. Wire Better Stack (off-box, → Telegram) + Uptime-Kuma (on-box) + cron-job.org 1-min keep-warm.

→ **Result: a working, self-healing, monitored, ~$10/yr public API. Zero Modal/pipeline work.**

**Stage 2, deliberately, when §2's triggers fire (build Path B):**
1. Build `OnnxBackend` + `model_store.py`; add the `[serve]` extra; bump `requires-python>=3.11`; wire `server.py`.
2. Build the Modal app: harvest (HN+RSS) → teacher-label (Modal T4) → distill (45-min cap, collapse firewall) → 5-tier eval gate → INT8 ONNX export → R2 delivery.
3. Stand up the R2 bucket + read-only Oracle token + `current.json` pointer.
4. Start **weekly** cadence ($0), `AUTO_PROMOTE=0` for 2-4 weeks, then flip `SENTILYZER_ML_BACKEND=onnx` on Oracle. The poller hot-swaps within one interval.

**Net: teachers today, student deliberately next.** The flywheel is Stage 2 of the roadmap (sequenced, near-$0, and the correct steady-state serving artifact for this box), **not** a rejected idea, and **not** an accuracy upgrade.