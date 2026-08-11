<!--
Generated 2026-07-18 by a multi-agent research pass (free hosting tiers, free GPU,
CPU training/inference feasibility, distribution mechanics), verified against live
sources on that date. Companion to continuous-training-plan.md (the paid Modal/Fly
design). This doc is the $0 / self-hosted / CPU-only counterpart.

Two honesty flags that run through the whole doc:
  - Free-tier facts (Oracle A1 specs, Kaggle GPU-hours, which hosts still have a free
    tier) change constantly and WILL drift: re-verify before relying on them.
  - The ultra-light pip[local] / INT8-ONNX / single-embedded-binary paths are DESIGNED
    but NOT yet built in this repo: the ML worker still hard-depends on torch. CPU
    training throughput numbers were measured on Apple Silicon; x86 runs ~2x slower.

The Terms-of-Service constraint from continuous-training-plan.md still holds when self-
hosting: only HackerNews + RSS may feed a durable training corpus. Going local does not
relax it. This is engineering research, not legal advice.
-->

# Making Sentilyzer completely free, self-hostable, and CPU-run

## 1. The answer, in 5 sentences

**Yes: a completely-free ($0/mo) Sentilyzer is realistic, and an ordinary CPU handles everything that matters day-to-day.** The Go gateway *already* compiles to a single 16 MB static file with no runtime, no interpreter, and no shared libraries (I built `CGO_ENABLED=0 … ./cmd/sentilyzerd` for both macOS/arm64 and Linux/amd64 just now: the Linux one is confirmed "statically linked, stripped"), and `make up` already clone-and-runs the whole stack over Docker, so "download-and-run" and "clone-and-use" are both real *today*. **CPU inference/classification is trivially fine, nightly teacher-labeling of ~10k docs is minutes-to-tens-of-minutes on a couple of cores, but full-corpus student *training* on a plain 8-core x86 laptop is a ~2-to-3-*day* job (your "40+ hours" suspicion is correct), so the training burst should go to a free GPU (Kaggle) or be subsampled to an overnight run.** The one genuinely "free-forever, always-on" host large enough for the full PyTorch teachers is Oracle Cloud's Always-Free A1 (2 OCPU / 12 GB). Fly.io's free tier is **dead** (trial only now), and Railway/Cloudflare-Containers/Deta are not $0. Two honesty flags up front: the ultra-light `pip install sentilyzer[local]` / INT8-ONNX / single-embedded-binary paths described in `docs/continuous-training-plan.md` are **designed but not yet built in this repo** (today the ML worker still hard-installs ~2 GB of torch), and the CPU-training throughput numbers below come from a high-end Apple-Silicon Mac, so a typical x86 laptop runs ~2× slower and an old CPU 3-5× slower.

---

## 2. The three tiers of "free"

### (a) Fully local: $0, no account, no cloud, your machine is the server
**What it gets you:** the entire product on your own laptop/desktop. Works *today* via `make up` (Docker) or by running the two processes directly (`make api-run` + `make ml-run`). Classification, the four transports (REST/JSON·XML·YAML, gRPC, GraphQL), on-demand `AnalyzeTopic`, and even training if you attach a trainer.
**The catch:** your machine has to be on to serve; there's no public URL unless you tunnel; and CPU training is slow (§3). The lightest no-download demo path is the **stub** (`SENTILYZER_ML_USE_STUB=1`), a pure-Python lexicon scorer (`ml/sentilyzer_ml/inference.py:63`) that needs no model and no GPU, but note the install caveat in §4.

### (b) Free cloud hosting: genuinely $0, but with real ceilings
| Host | Free spec | Fits the teachers (~2-2.5 GB)? | The catch |
|---|---|---|---|
| **Oracle Cloud Always-Free A1** | 2 OCPU / **12 GB** / 200 GB block, **indefinite** | **Yes**: the only always-warm free box big enough | Cut from 24 GB→12 GB **June 2026**; "out of capacity" errors at create time (automate retries); **idle-reclamation** if CPU *and* network *and* memory are all <20% over 7 days |
| HF Spaces (CPU Basic) | 2 vCPU / 16 GB | Yes | **Sleeps after 48 h idle**, demo-shaped, storage ephemeral; new-account access to CPU-Basic is reportedly being restricted |
| GCP Cloud Run (always-free) | scale-to-zero, us-central1/east1/west1 | No: student only | Cold starts on idle; can't fit the 2 GB teachers in the free vCPU-s budget |
| GCP e2-micro VM | 1 GB RAM, 24/7 | No: student only, tight | 1 GB barely holds the ONNX student |

**The catches that decide everything:** RAM ceilings (only A1 and HF Spaces clear the *teacher* bar); almost everything except A1 sleeps/scale-to-zero (cold-start latency); and A1's reclamation rule is *ANDed*, so a resident loaded teacher keeps memory >20% and protects the box, **but a student-only box (~0.3-1 GB) can sit *below* the 2.4 GB memory floor, so a keep-alive self-ping cron is *mandatory*, not optional.**
**Do NOT count as free:** Fly.io (trial only: 2 VM-hours or 7 days), Railway (~$1/mo min after trial), Cloudflare Containers ($5/mo Workers-Paid), Deta (shut down 2024-10-17), SageMaker Studio Lab (closing to new signups 2026-07-30).

### (c) Free GPU for the periodic training burst
| Option | Free budget | Automatable unattended? | Catch |
|---|---|---|---|
| **Kaggle Notebooks** | **~30 GPU-hr/week**, 12-h sessions, P100 16 GB or 2×T4 | **Yes**, but via `kaggle kernels push` from an external cron, **not** the built-in scheduler | Built-in scheduler is **CPU-only** (the #1 trap); GPU+internet need one-time **phone verification** |
| Google Colab free | ~15-30 dynamic T4-hr/wk | **No** (manual only) | GPU not guaranteed at US-daytime peak |
| Lightning AI | ~15 credits (~80 GPU-hr)/mo | Yes (Jobs SDK) | Credit-limited |
| HF Hub | free public model + dataset repos | n/a | The right place to *store* the ~80 MB student and the corpus |

The **automatable $0 loop**: a free GitHub Actions cron (unlimited minutes on public repos) runs `pip install kaggle && kaggle kernels push --accelerator NvidiaTeslaT4` (with `enable_internet=true`, HF token as a Kaggle Secret) → the notebook trains + exports INT8 ONNX + pushes to HF Hub → your serving box pulls it. *(The Kaggle "30 hr/week" figure is corroborated across current sources but sits on a JS-rendered page the researchers couldn't read verbatim today. Treat as "very likely," not gospel; the CLI push/metadata behavior **was** read off official docs.)*

---

## 3. What a CPU can and cannot do

| Task | CPU-feasible? | Real number | Hardware needed | Escape hatch |
|---|---|---|---|---|
| **Classify / inference** (serving) | **Yes, easily** | INT8-ONNX student **~17 ms/batch warm** (design target; the event-driven doc measured session-init 6.2 s + infer 0.017 s at Modal's starved 0.125-core). Raw fp32 82 M ~156 seq/s, ~214 docs/s on an M4 Pro; runs on a Raspberry Pi 5 (~42 ms) | Any modern CPU, ~1-2 GB RAM; set `intra_op_num_threads` (ORT ignores cgroup quotas) | none needed |
| **Teacher-labeling** ~10k docs/day | **Yes** | ~2-4 min/day on a strong Mac CPU; **~11-40 min** on a couple of cores / the Oracle 2-OCPU box. *(Scaled from measured 82 M to 125 M RoBERTa, marked `[likely]`, not directly benchmarked)* | Multi-core CPU, ~2-4 GB RAM | free GPU does it in <1 min if preferred |
| **Student distillation TRAINING** | **Yes but SLOW: the one weak spot** | See below | 8 GB works for a small/subsampled job (16 GB comfortable) | **free GPU or Mac MPS or subsample** |

**The blunt truth on CPU training** (adopting the skeptic's correction: the measured rates are from a 14-core Apple-Silicon Mac with AMX, so scale for your hardware):

| Machine | 82 M student | 22 M MiniLM | Full 900k-example × 3 epochs (82 M) |
|---|---|---|---|
| 14-core Apple Silicon (measured) | 27 seq/s | 57 seq/s | **~28 h** |
| **Typical 8-core x86 laptop (~2× slower)** | ~12-13 seq/s | ~28 seq/s | **~55-62 h ≈ 2.5 days** ← your "40+ hours" hunch, confirmed |
| Old / low-end CPU | 3-5× slower | 3-5× slower | multi-day |
| Same Mac's **MPS GPU** (free, Apple-only) | 104 seq/s (~4×) | 270 seq/s | **~7 h overnight** |
| Free **Kaggle T4/P100** | n/a | n/a | **<1 h** |

So on a plain x86 8-core, what actually fits an **~8-hour overnight window** is only the *subsampled* job: **~100-115k examples × 3 epochs (82 M)** or **~200-230k (MiniLM-22M)**. Full-corpus daily training on a bare CPU is **not** realistic. Two more honest caveats: the 27 seq/s is a raw-encoder *floor* (a real HuggingFace `Trainer` loop with tokenization/dataloading is somewhat slower), and the measured 4.3 GB peak assumes soft labels are **precomputed**: if the 125 M teacher is loaded *alongside* the training student, RAM rises to ~7 GB, which is **tight on an 8 GB machine**. RAM is otherwise a non-issue (82 M peaks ~4.3 GB, 22 M ~2.5 GB: the "needs 25+ GB or it crashes" scare does not apply to a 6-layer student streamed in batches).

**Verdict:** inference and labeling are solved on CPU; training is "overnight-weekly if you subsample or own a Mac, otherwise push the burst to a free GPU." The design's **wall-clock / sequence cap** (`MAX_TRAIN_SECONDS`, `MAX_TRAIN_SEQUENCES` in the Modal plan) is exactly the lever that makes a bounded CPU/GPU run feasible. Keep it. And keep the **collapse firewall** (re-init the student from the *frozen teacher* every run, never from yesterday's student) in any local variant.

---

## 4. Distribution options, ranked (closest to "one-command download-and-run" → "clone the repo")

I've tagged each with **STATUS** because the honest repo state differs from the aspirational design.

**Rank 1: Single static Go binary (the gateway).** ✅ **Works today.** `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"` → **16 MB**, one file, no runtime, cross-compiles to macOS/Linux/Windows × arm64/amd64 (verified this session). *Ships:* the gateway + all four transports. *Includes the model?* **No**: it calls the ML worker over gRPC, or serves the mock connector standalone. *Can it train?* No. *Tradeoff:* it's the front door, not the brain. It needs the ML worker (or the future embedded-ONNX path) to score real text.
- *Designed-but-unbuilt flagship:* embed the INT8 ONNX student *inside* the Go binary via **Hugot's pure-Go GoMLX backend** so one file classifies with **no Python at all**. This is the most attractive and least-proven path (GoMLX pure-Go is "slower than XLA," static-shapes-only until ~mid-2026, SIMD only on MatMul). **Prototype against the actual distilroberta/MiniLM student before promising it.** Fallback: ship one `libonnxruntime.so` in the tarball and use Hugot's ORT backend.
- *Release automation:* not set up yet. The repo has only `.github/workflows/ci.yml`. **GoReleaser** (free for public repos) → GitHub Releases + a personal `homebrew-tap` is the ~half-day of work that turns this into `brew install` / download-the-asset.

**Rank 2: Docker one-liner / `make up`.** ✅ **Works today** (`docker compose -f deploy/compose/docker-compose.yml up --build`, gateway + Python ML worker over gRPC on :50051). *Includes the model?* The stub (no download) or the real HF teachers (downloaded on first call, ~2 GB). *Can it train?* Not as-is; add a torch trainer. *Tradeoff:* needs Docker; the current `ml.Dockerfile` is heavy (installs torch). **Two fixes to make it truly turnkey:** (1) publish prebuilt images to **GHCR** (free, public) so it's `docker run ghcr.io/…` with no build/account; (2) split a light **ONNX-only** image (~200-400 MB, no torch) from a `--profile train` image. *(`deploy/docker/api.Dockerfile` was already fixed: it now builds `CGO_ENABLED=0` on a distroless static base with no `build-base`.)*

**Rank 3: pip / uvx package (the ML worker).** ⚠️ **Partially real.** `pip install ./ml` works today **but pulls ~2 GB of torch**: `ml/pyproject.toml:20` lists `torch>=2.4` as a *hard* dependency, and there is **no `[local]` extra and no onnxruntime anywhere in the repo yet.** The lightweight `pip install sentilyzer[local]` (onnxruntime + tokenizers + numpy, ~40-65 MB, **no torch**) that the docs describe **needs building**: add the extra, add an onnxruntime backend next to `TransformerBackend`, and produce the ~80 MB INT8 ONNX artifact. Once published to PyPI, `uvx sentilyzer` / `pipx install 'sentilyzer[local]'` give a zero-config one-command install. *Note:* the stub is torch-free at **runtime** (torch is imported lazily inside `_load_general`/`_load_aspect`, `inference.py:139,156`) but **not at install time**, so "clone and run the stub with no torch" today means installing just `grpcio`+`numpy` by hand, not `pip install ./ml`.

**Rank 4: git clone + `make`.** ✅ **Works today**, most capable, least turnkey. Full stack, and it's the only shape that can *train*. Needs Go + Python + protoc (or just Docker via `make up`).

**Prebuilt GitHub Release?** Not yet: designed via GoReleaser, free for public repos, 1000 assets/release at <2 GiB each. Attach the per-OS Go binaries + the ~80 MB ONNX student here.

---

## 5. The recommended completely-free setup (for a solo dev who wants $0)

**Cleanest fully-$0 topology (one box + a free GPU burst):**

1. **Serve + label on ONE always-warm box.** Either your **own laptop/desktop** (`make up`, or the two processes) or **one Oracle A1 free VM** (12 GB, indefinite, always-on). Run the Go gateway + the ML worker there; do nightly **teacher-labeling on CPU** (minutes/day, no GPU). If you use A1 student-only, add the **self-ping cron** (mandatory) to dodge idle-reclamation.
2. **Push the training burst to Kaggle's free GPU.** Put a training notebook + `kernel-metadata.json` (`enable_gpu=true`, `NvidiaTeslaT4`, `enable_internet=true`) in the repo. A **free GitHub Actions cron** runs `kaggle kernels push` weekly → the notebook re-inits the student from the frozen teacher, trains (<1 h, well inside 30 GPU-hr/week), quantizes to **INT8 ONNX**, and pushes the ~80 MB artifact to a **free HF Hub model repo**. Your serving box polls HF Hub, verifies the hash, and **hot-swaps** (no redeploy).
3. **Store the corpus + artifact free:** HF Datasets/Models, or Kaggle's 20 GB persistent, or Oracle's 200 GB block volume, or Cloudflare R2 (10 GB free, zero egress).

**How training actually happens, three honest options ranked:**
- **Free-GPU burst (recommended default):** Kaggle T4/P100, <1 h/run, fully automatable, $0.
- **Mac overnight (if you own Apple Silicon):** MPS backend, ~4× your CPU: even full-corpus 82 M × 3 epochs is a ~7 h overnight run; MiniLM is ~2.8 h.
- **Plain-CPU overnight (last resort):** only if you **subsample to ~100-200k examples, 1-2 epochs, prefer the 22 M MiniLM student**, and optionally IPEX/bf16 + layer-freezing. Daily full-corpus CPU training is off the table (§3).

Net: nobody pays anything. The only "cost" is cadence: full-corpus training is weekly-overnight (or a free-GPU burst), not real-time. One-time chores: Kaggle phone-verification, and storing `KAGGLE_KEY` + an HF token as secrets.

---

## 6. What changes vs the paid Modal/Fly plan (`docs/continuous-training-plan.md`)

| Concern | Paid plan (`$19.30/mo` expected) | Completely-free version | Note |
|---|---|---|---|
| **Student serving** | Modal CPU, `min_containers=1`, ~$10.26/mo | **Local machine** or **Oracle A1** free VM, $0 | A1 is the only always-warm free box; else cold starts |
| **Gateway host** | Fly `shared-cpu-1x`, $3.32/mo | Same box as serving, $0 | **Fly's free tier is gone**: no $0 like-for-like swap |
| **Training burst** | Modal A10, ≤45 min/run, $30.18/mo daily ($13.12 after credit) | **Kaggle free GPU** (~30 GPU-hr/wk) / **Mac MPS** / **CPU-subsampled**, $0 | Cadence bounded by quota or CPU speed, not cash |
| **Teacher labeling** | Modal T4, ~$1.11/mo | **Local CPU nightly**, $0, minutes/day | Answers the plan's open "does the teacher need a GPU?": **no** |
| **Scheduler** | Modal Cron (2 of 5) | **GitHub Actions cron** (free, public) driving `kaggle kernels push`; or local `cron`/`launchd` | Kaggle's own scheduler is CPU-only (can't be the trainer) |
| **Model delivery** | Cloudflare R2 `current.json` pointer ($0) | **HF Hub** model repo (free) or R2 or a file on the box | Poll + hash-verify + hot-swap unchanged |
| **Corpus + aggregates** | R2 + Neon (~$1.66/mo) | HF/Kaggle Datasets, Oracle volume, or local parquet/SQLite | $0 |
| **Monitoring** | healthchecks/Axiom/BetterStack free | Same, or just local logs | $0 either way |
| **TOTAL** | **$19.30/mo** (band $6.18-$47.36) | **$0/mo** | Tradeoffs: cold serving unless a box stays warm (A1); training cadence gated by free-GPU quota / CPU speed; A1 out-of-capacity + reclamation friction |

**What stays identical (do not change these going free):** the CGO-free Go gateway and its four transports; the **ToS compile-time gate** (only HackerNews + RSS may enter a durable training corpus: Reddit/X/StockTwits/YouTube stay blocked; going local does **not** relax this); the **collapse firewall** (re-init student from frozen teacher every run); the INT8-ONNX serving path; and the **wall-clock / sequence training cap**, which matters *more* on a slow CPU, not less.

**Bottom line:** all three of your questions are "yes," with one caveat each. Completely free: **yes**, fully local or on Oracle's always-free A1, training on a free GPU. Download/clone-and-run: **yes today** for the static Go gateway (16 MB, verified) and the `make up` stack; the ultra-light pip/ONNX/single-embedded-binary variants are designed and need ~a day of build work. CPU can do the work: **yes** for classification and labeling, and **yes-but-slowly** for training, where a normal laptop is a multi-day job and the honest move is a free-GPU burst or an overnight subsampled run.