# Model Canary Controller — Build Spec

A control plane that rolls out a new model version to a fraction of traffic, compares it against the incumbent on latency, cost, and quality, and automatically rolls back if the candidate is worse — without ever diffing outputs directly.

Runs entirely locally at $0. Open-weight models, synthetic data, personal accounts only.

---

## 1. Fixed design decisions

These are settled. Everything else in the spec follows from them.

- **Trigger-agnostic.** The controller detects a regression from *any* cause — prompt change, config change, quantization, fine-tune. It watches metrics and never inspects the cause. Multiple injection fixtures exist to prove this property.
- **Non-inferiority, not superiority.** The canary's job is to prove the candidate is *not worse*, not that it's better. That's what shapes the statistics. (Proving "better" is A/B testing's job.)
- **Windows decide, not requests.** A single response from a stochastic model carries no signal. All verdicts come from an accumulated window meeting a minimum sample size.
- **Control plane is model- and trigger-independent.** It consumes responses and metadata through a stable gRPC interface. No model or fixture specifics leak into decision logic.
- **Local build, Docker for reproducibility.** No cloud hosting. `docker compose up` is the portability story.

---

## 2. Architecture

Three views, because one diagram can't express components, time, and a request trace at once. Use the same split in the README.

### 2.1 Components

```
Traffic generator
  replays fixed synthetic prompts at controlled RPS
        │
        │ gRPC
        ▼
CONTROL PLANE  (Go)
  1. Canary orchestrator     owns current split % and phase
  2. Traffic splitter        routes by weight, TAGS each response
  3. Guardrail evaluator     accumulates a window, metrics PER VARIANT
  4. Decision engine         non-inferiority verdict
        │
        │ gRPC
        ├───────────────────────┐
        ▼                       ▼
  INCUMBENT worker        CANDIDATE worker
  baseline model          regression config (A or B)
        │                       │
        └───────────┬───────────┘
                    │ responses written to log
                    ▼
          Quality scorer  (Python, OFFLINE)
          reads the log after the fact, not in the request path
```

- **Traffic generator** — fixed synthetic prompt set at controlled RPS. Fixed set makes runs comparable.
- **Canary orchestrator** — the only component with a sense of time. Owns split weight and phase.
- **Traffic splitter** — routes each request by weight and **tags the response with its variant**. Without the tag, metrics can't be attributed and the design collapses.
- **Guardrail evaluator** — accumulates tagged responses into a window; computes every metric **separately per variant**. Output is two parallel metric sets.
- **Decision engine** — takes both sets, answers "is the candidate not materially worse?" Emits pass / keep collecting / fail.
- **Model workers** — identical gRPC servers; the only difference is config (prompt template, quant level). They don't know they're in a canary.
- **Quality scorer** — runs **offline** over logged responses. Embedding similarity needs a second model; running it inline would add latency and pollute the latency measurements.

### 2.2 Rollout states

```
  Baseline only        100% incumbent; candidate deployed but unexposed
        │
        ▼
  Canary @ X%          splitter begins routing X% to the candidate
        │
        ▼
  Evaluate window      accumulate until >= min sample size, then compare
        │
        ├──▶ verdict WORSE      ──▶  ROLLBACK
        │                             split -> 0% immediately
        │
        └──▶ verdict NOT WORSE  ──▶  ADVANCE
                                      raise split, re-enter Evaluate
                                      10% -> 25% -> 50% -> 100% -> promoted
```

- **Baseline only** — candidate deployed but receives no traffic; verify health before exposing anyone.
- **Evaluate window** — enforces the minimum sample size. This is what prevents rolling back on five noisy requests.
- **Advance** — re-earn confidence at each exposure level rather than jumping 10% → 100%.
- **Rollback** — a *routing change*, not a redeployment. Candidate stays deployed at 0% traffic, preserved for debugging.

### 2.3 One request's trace

Generator sends prompt #47 → splitter reads current weight, routes to candidate → worker returns response → splitter tags `variant=candidate`, records latency and token count → response written to log → evaluator adds sample to the candidate bucket (now 63 candidate / 560 incumbent). **Nothing else happens.** When the window hits minimum-N, the evaluator computes both metric sets, the decision engine returns a verdict, and the canary orchestrator advances or rolls back. Separately, the offline scorer reads logged responses and computes semantic quality.

### 2.4 Shadow vs. canary

Shadow mirrors traffic with no user-facing effect; canary splits real traffic by weight. Production practice is shadow first, then canary. Implement canary; note in the README that shadow is a config flag away.

---

## 3. Variants and injection fixtures

One incumbent baseline; each candidate is evaluated against it as an **independent canary run**. Never canary two candidates against each other.

| Variant | Simulates | Expected failure guardrail |
|---|---|---|
| **Incumbent** | Current production model | — (reference) |
| **Candidate A** — prompt/config regression | The most common real trigger | Refusal-rate spike **or** length collapse |
| **Candidate B** — quantization regression | Cost-optimization deployment | Semantic-quality drop at **flat latency** |
| **Control** — incumbent vs. itself | A healthy canary | None; must promote |

**The two regressions must fail on different guardrails.** That's the point — it proves the detector measures quality generally rather than one mechanism's artifact. If both merely lower semantic similarity, collapse to two variants and save the effort.

**Injection details:**
- **Candidate A** — degrade the system prompt or retrieval config on the *same* model: strip instructions, inject a contradictory directive, truncate context. Aim it at refusal rate or output length.
- **Candidate B** — same model at a lower quant level (Q3/Q2 vs Q8 incumbent). Real degradation, roughly flat latency. Over-aggressive quantization is a documented production risk (Q3 and below shows visible reasoning degradation), so this is a legitimate scenario, not a contrivance.
- **Control** — candidate identical to incumbent. Run this first; it measures the false-positive rate and validates the apparatus.

**Evaluate one candidate at a time.** Simultaneous three-way splits introduce the multiple-comparison problem and muddy attribution. Worth mentioning as future work; don't build it.

---

## 4. Tech stack

| Layer | Choice |
|---|---|
| Control plane | Go + gRPC |
| Model workers | Python + gRPC |
| Inference engine | Ollama (primary) or llama-cpp-python |
| Model | Qwen2.5-0.5B or SmolLM-360M |
| Quality scoring | sentence-transformers (`all-MiniLM-L6-v2`), scipy |
| Drift reporting | Evidently (committable HTML reports) |
| Metrics | Prometheus client (Go + Python), Grafana optional |
| Plots | matplotlib; Plotly optional |
| Packaging | Docker + docker-compose |
| Python deps | uv (`pyproject.toml` + `uv.lock`) |

No C++ required — the inference engine is consumed as a pre-built binary or HTTP API, like Postgres or Redis.

**Validate the Candidate B fixture early.** On very small models the Q8-vs-Q2 gap can be noisy or can also move latency. If you don't get a clean "quality drops, latency flat" separation, switch Candidate B to forced truncation or probabilistic refusal — then re-check it still fails distinctly from Candidate A.

---

## 5. Guardrail metrics

Match the metric set production LLM canaries actually track. Wire 3–4; different candidates should breach different ones.

- **Latency percentiles** — p50/p95/p99 (TTFT, TPOT). Percentiles, not averages; LLM latency distributions are heavily skewed.
- **Cost per request** — token counts shift with model and prompt versions. First-class guardrail.
- **Refusal / error rate** — categorical. Candidate A's likely trip wire.
- **Output-length distribution** — catches mode collapse and runaway verbosity. Candidate A's other trip wire.
- **Semantic quality** — cosine similarity to a golden reference. Candidate B's trip wire; the drop latency can't see.

**Why outputs can't be diffed:** even at temperature 0 with greedy sampling, LLM inference is non-deterministic in practice — GPU floating-point operations aren't strictly associative and batch-size variability changes rounding, producing documented accuracy variation across identical-input runs. Hence windowed, distributional comparison.

---

## 6. Decision engine

The question is always: **is the candidate not materially worse than the incumbent?**

**MVP.** Windowed comparison with a **minimum sample size** before any verdict, and a two-sample test per guardrail:
- Mann-Whitney U for latency distributions
- Proportion / chi-square for refusal rate
- KS test for length distribution
- A non-inferiority margin on mean semantic similarity

Roll back if any guardrail shows the candidate worse beyond its margin; promote if all clear after the window.

**Multiple testing.** Monitoring several guardrails inflates the chance of a spurious rollback. Control it with a correction or per-guardrail margins tuned against the control run.

**Sequential testing (discuss; optionally implement).** SPRT or CUSUM decides as early as the evidence allows rather than waiting for a fixed window, exposing fewer requests to a bad candidate. If you implement it, verify the stopping-rule math against a proper reference before publishing numbers — sequential tests are easy to get subtly wrong. Discussing it as future work is safe and still signals the depth.

---

## 7. Experiments

1. **Control** (incumbent vs. itself) → expect promote. Measures false-positive rate. **Run first.**
2. **Candidate B** (quantization) → expect rollback on semantic quality, latency clean. The silent-regression case.
3. **Candidate A** (prompt/config) → expect rollback on refusal rate or length. Different guardrail than B.
4. **Comparison matrix** — all three side by side. This is the proof of cause-agnostic detection and the single most important result.
5. *(Stretch)* Sensitivity sweep — vary the non-inferiority margin; plot false-positive vs. false-negative.
6. *(Stretch)* Time-to-decision — requests needed for a confident verdict.

**Centerpiece visual:** a two-panel timeline per regressed candidate. Top panel, the tripping guardrail for incumbent vs. candidate over the window. Bottom panel, the traffic split. Vertical line at the rollback moment. The pair — two different guardrails tripping — *is* the story. Top of the README.

---

## 8. Observability

Priority order, and they are not equal:

1. **Result plots** (matplotlib) — essential. How a reviewer sees the detector made the correct decision without running anything.
2. **Prometheus instrumentation** — real code in the repo: guardrail metrics, decision counts, rollback events on a `/metrics` endpoint.
3. **docker-compose** — brings the whole stack up with one command.
4. **Grafana dashboard** — committed JSON + screenshot. Optional polish; drop it first if trimming.

Evidently HTML drift reports are a strong committed artifact given the quality-regression headline.

**Sequencing:** get the detector working and produce one ugly plot proving a correct rollback *before* investing in presentation.

---

## 9. Reproducibility

- **docker-compose** for the full stack — this replaces cloud hosting as the production-thinking signal.
- **Apple Silicon:** develop on ARM64 but keep containers CPU-only and x86-reproducible. Don't let results depend on Metal/MPS.
- **State benchmark hardware** in the writeup (chip, cores, RAM).
- **Seed** what you can; document what's inherently stochastic.
- **Pin versions:** commit `uv.lock` with `pyproject.toml`, and pin model tags/hashes. A different `sentence-transformers` build shifts golden-set similarity scores and silently invalidates cross-run comparisons.

*Optional:* write Kubernetes manifests and a short "how this deploys to GKE with Argo Rollouts" README section without running it. Same knowledge signal at $0.

---

## 10. Repo structure

Items marked `✓` already exist; everything else is planned.

```
model-canary-controller/
├── README.md                          ✓
├── Makefile                           ✓
├── buf.yaml                           ✓
├── pyproject.toml                     ✓
├── uv.lock                            ✓
├── docker-compose.yml                 # control plane + workers + prometheus
├── common/                            ✓ shared proto types
│   ├── buf.gen.yaml                   ✓
│   └── proto/
│       ├── infer.proto                ✓
│       ├── types.proto                ✓
│       └── generated/                 ✓ infer_pb2.py, infer.pb.go, types_pb2.py, types.pb.go
├── services/
│   ├── controller/                    # Go — model/trigger-agnostic control plane
│   │   ├── buf.gen.yaml               ✓
│   │   ├── proto/
│   │   │   ├── controller.proto       ✓
│   │   │   └── generated/             ✓ controller.pb.go, controller_grpc.pb.go
│   │   ├── orchestrator/              # rollout phases + current split %
│   │   ├── splitter/                  # weighted routing, tags each response
│   │   ├── evaluator/                 # windowed metrics, computed per variant
│   │   ├── decision/                  # non-inferiority engine
│   │   └── metrics/                   # prometheus instrumentation
│   ├── worker/                        # Python — gRPC server + injection
│   │   ├── buf.gen.yaml               ✓
│   │   ├── proto/
│   │   │   ├── worker.proto           ✓
│   │   │   └── generated/             ✓ worker_pb2.py, worker_pb2_grpc.py
│   │   ├── server.py                  # gRPC server entrypoint
│   │   └── regression/                # Candidate A (prompt), B (quant)
│   ├── scorer/                        # offline: embedding similarity, refusal, length
│   └── trafficgen/                    # replays prompt set at controlled RPS
├── data/
│   ├── prompts.json                   ✓
│   └── golden.json                    ✓
├── experiments/                       # one script per experiment in §7
│   ├── 01_control.py
│   ├── 02_candidate_b.py
│   ├── 03_candidate_a.py
│   └── 04_comparison_matrix.py
├── results/                           # committed PNGs, Evidently HTML reports, screenshots
├── grafana/                           # committed dashboard JSON + screenshot
└── k8s/                               # (optional) GKE manifests, unrun
```

---

## 11. Scope

**MVP — a complete project on its own:**

- Control plane: canary orchestrator, splitter, evaluator, non-inferiority decision engine (fixed window, min sample size)
- Incumbent + candidate workers with config-selected injection
- Candidate B and the control run
- Guardrails: p99 latency, refusal rate, semantic similarity, cost per request
- Experiments 1 and 2, plus the two-panel plot
- docker-compose, README, blog post

**Stretch, in priority order:**
1. **Candidate A + the three-variant matrix** — the cause-agnostic proof. Promote to MVP if time allows.
2. Evidently drift report
3. Prometheus + Grafana with committed screenshot
4. Sensitivity sweep
5. SPRT / sequential engine
6. Time-to-decision experiment
7. GKE manifests (unrun); shadow-mode flag

Candidate A is deliberately the first stretch rather than MVP: the three-variant matrix is additive rather than load-bearing, so it can be built incrementally after the core is solid.

---

## 12. Plan

**Phase 1 — plumbing and fixture validation.** gRPC contracts; both workers responding; traffic generator at controlled RPS; raw latency measured. Validate the Q8-vs-Q2 separation and switch fixtures now if it doesn't hold. Goal: requests flowing through a dumb splitter.

**Phase 2 — the substance.** Guardrail evaluator; canary orchestrator; non-inferiority decision engine with min sample size. Run experiments 1 and 2. Produce one ugly plot proving detection. Goal: the controller makes a correct verdict. *This phase is the project.*

**Phase 3 — land it.** Add Candidate A aimed at a different guardrail; build the comparison matrix and polished plots; docker-compose; README and blog draft.

---

## 13. Writeup

**README** (60-second read): centerpiece plots at top; one-line pitch; the results matrix; `docker compose up`; benchmark hardware; link to the blog. Explain architecture using the §2 three-view split, not one combined diagram.

**Blog post** — working title: *"Automatically rolling back a bad model version when you can't diff the outputs."*

1. **The problem** — promoting a new model version safely when responses are non-deterministic, and "new version" can mean a fine-tune, a prompt change, or a cheaper quantized build.
2. **Why the obvious approaches fail** — diffing is meaningless; latency monitoring misses quality regressions; a threshold on five requests is noise.
3. **Non-inferiority, not superiority** — and how that reframes the statistics.
4. **Guardrail design** — the metric set and which failure each catches.
5. **The decision engine** — windowed tests, minimum sample size, the multiple-testing hazard, why sequential testing is the better long-term answer.
6. **The experiments** — the matrix. Healthy promotes; quantization rolls back on quality with flat latency; prompt change rolls back on refusal/length. Two triggers, two guardrails, one controller.
7. **The postmortem** — a canary that rolled back when it shouldn't have, and how you tuned the margin. Worth more than the happy path.
8. **At scale** — GKE + Argo Rollouts sketch, sequential testing, simultaneous multi-variant rollout and its multiple-comparison hazard, shadow-then-canary.

Write the generalizable pattern throughout. Never reference your employer's implementation.
