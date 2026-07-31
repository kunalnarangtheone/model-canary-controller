# Traffic Generator — Design Document

## 1. Role in the system

The traffic generator is the entry point for every experiment. It:

1. Loads the fixed synthetic prompt set from `data/prompts.json`.
2. Replays those prompts at a controlled RPS by calling `ControllerService.Infer` over gRPC.
3. Collects the `InferResponse` for each call and writes it to a line-delimited JSON log so the offline scorer can read it later.
4. Exposes a `/metrics` Prometheus endpoint with per-prompt-type counters, latency histograms, and error rates.

The generator is **dumb by design**: it does not set `variant`, does not know the split weight, and does not make decisions. The controller's splitter assigns variant and routes. The generator's only job is to produce a steady, reproducible request stream.

---

## 2. Fixed design decisions

- **Fixed prompt set, sequential replay.** Prompts are replayed in a deterministic order (round-robin across the full set) rather than sampled randomly. This makes runs comparable: the same prompt mix hits both variants, so any difference in metrics is attributable to the variant, not to prompt selection luck.
- **Controlled RPS, not fire-and-forget.** A token-bucket rate limiter paces requests. The target RPS is a CLI flag. This prevents the generator from flooding small local workers and also lets experiments document the load they ran at.
- **Blocking calls only.** Each goroutine sends one request, waits for the response, records it, then picks the next prompt. No async batching. This keeps latency measurements honest: `latency_ms` in `InferResponse` is the worker's internal measurement, and wall-clock round-trip is also recorded for end-to-end visibility.
- **Variant field left unset.** The generator sends `VARIANT_UNSPECIFIED` (proto default). The splitter owns assignment. Letting the generator set variant would couple it to experiment configuration and break the control-plane-is-trigger-independent invariant.
- **Log format: line-delimited JSON.** One JSON object per response, written to a file named `<experiment_id>_<timestamp>.jsonl`. The offline scorer reads this file; no shared database.
- **Written in Go.** Same language as the controller. Uses the generated `controller.pb.go` / `controller_grpc.pb.go` stubs directly. No separate process boundary.

---

## 3. Interfaces

### 3.1 gRPC — outbound

```
service ControllerService {
  rpc Infer(common.InferRequest) returns (common.InferResponse);
}
```

The generator calls `ControllerService.Infer`. It populates:

| Field | Value |
|---|---|
| `prompt_id` | Stable ID from `prompts.json` (e.g. `r007`) |
| `prompt_text` | Full prompt text |
| `variant` | `VARIANT_UNSPECIFIED` (0) — splitter fills this in |

The response carries `variant`, `latency_ms`, `input_tokens`, `output_tokens`, `refused`, and `error` — all written verbatim to the log.

### 3.2 Log file — outbound

Each line is a JSON object:

```jsonc
{
  "experiment_id": "02_candidate_b",
  "seq": 1234,                    // monotonically increasing request number
  "prompt_id": "r007",
  "prompt_type": "reasoning",
  "variant": "CANDIDATE_B",       // string form of the enum
  "response_text": "...",
  "input_tokens": 18,
  "output_tokens": 42,
  "latency_ms": 310,              // worker-reported, end-to-end of inference
  "wall_ms": 328,                 // generator-measured round-trip including gRPC overhead
  "refused": false,
  "error": "",
  "ts_unix_ms": 1722000000000     // wall clock at response receipt
}
```

`error` is non-empty when the worker or transport failed; the evaluator treats these as dropped samples. The offline scorer skips them for quality metrics but counts them for error rate.

### 3.3 Prometheus — outbound

Endpoint: `http://0.0.0.0:9101/metrics` (port distinct from the controller's `9100`).

| Metric | Type | Labels | Purpose |
|---|---|---|---|
| `trafficgen_requests_total` | Counter | `prompt_type`, `variant`, `status` | Throughput per variant/type |
| `trafficgen_latency_ms` | Histogram | `prompt_type`, `variant` | End-to-end round-trip distribution |
| `trafficgen_refusals_total` | Counter | `prompt_type`, `variant` | Mirrors controller's guardrail |
| `trafficgen_errors_total` | Counter | `error_kind` | Transport vs. worker errors |

`status` ∈ `{ok, refused, error}`. `error_kind` ∈ `{transport, worker}`.

---

## 4. Configuration

All flags passed via CLI (or environment variables with the same name uppercased, for Docker compatibility):

| Flag | Default | Description |
|---|---|---|
| `--controller-addr` | `localhost:50051` | gRPC address of ControllerService |
| `--prompts-file` | `data/prompts.json` | Path to the prompt set |
| `--rps` | `5` | Target requests per second |
| `--duration` | `0` | Run for N seconds then stop; 0 = run until interrupted |
| `--concurrency` | `4` | Number of parallel sender goroutines (≤ rps makes sense as a ceiling) |
| `--log-file` | `results/<experiment_id>_<ts>.jsonl` | Output log path; `-` for stdout |
| `--experiment-id` | `experiment` | Embedded in each log line and the filename |
| `--metrics-addr` | `0.0.0.0:9101` | Prometheus scrape endpoint |
| `--prompt-types` | `factual,reasoning,instruction` | Comma-separated filter; omit for all |

---

## 5. Internal structure

```
services/trafficgen/
├── main.go              # flag parsing, wiring, shutdown
├── loader.go            # reads and validates prompts.json
├── sender.go            # one goroutine per concurrency slot; token-bucket pacing
├── logger.go            # writes JSONL log; buffered, flushed on shutdown
└── metrics.go           # Prometheus registration and helpers
```

### 5.1 Prompt replay

```
promptQueue chan Prompt  // unbuffered; sender goroutines pull from it
```

A single `feeder` goroutine iterates the prompt slice in order, cycling indefinitely, and writes to `promptQueue`. Sender goroutines pull one prompt at a time, call `Infer`, and push the result to the log. The rate limiter sits in the feeder: it sleeps after each push to maintain the target inter-arrival time.

Round-robin (not random) ensures every window sees the same prompt-type ratio, which matters because reasoning prompts stress Candidate B differently from factual prompts.

### 5.2 Graceful shutdown

On `SIGINT` / `SIGTERM`:
1. Close `promptQueue` — feeder exits, senders drain.
2. Wait for all in-flight RPCs to complete (with a 10-second hard deadline).
3. Flush the log writer.
4. Print a summary line: total sent, total errors, actual average RPS.

### 5.3 Rate limiting

Uses a simple token-bucket: one token per `1/rps` second, replenished in the feeder loop. If the controller is slower than the target RPS (worker overwhelmed), the feeder backs up and effective RPS drops — this is intentional. The generator never drops requests to hit a number; it reports actual throughput via Prometheus.

---

## 6. Error handling

| Condition | Behaviour |
|---|---|
| gRPC transport error | Log `error_kind=transport`; increment `trafficgen_errors_total`; write log line with `error` field set; continue |
| Worker returns non-empty `error` field | Log `error_kind=worker`; increment counter; write log line; continue |
| Controller unreachable at startup | Retry with exponential backoff for 30 s, then exit with a clear message |
| `prompts.json` missing or malformed | Exit immediately with a descriptive error — no retries |
| Log file write error | Exit immediately — a partial log is worse than no log |

---

## 7. Testing

| Test | What it checks |
|---|---|
| `TestLoader_ValidFile` | Parses `data/prompts.json`; asserts 60 prompts, correct type distribution |
| `TestLoader_MissingFile` | Returns a wrapped error, not a panic |
| `TestRoundRobin` | After N × len(prompts) sends, each prompt has been sent exactly N times |
| `TestRateLimit` | Sends 10 prompts at 2 RPS against a mock controller; wall time is 4–6 s |
| `TestGracefulShutdown` | Cancel context mid-run; assert all in-flight RPCs drained, log flushed |
| `TestLogLine` | Parses a written JSONL line; asserts required fields present and correctly typed |
| `TestMetrics` | Scrape `/metrics` after 5 calls; assert `trafficgen_requests_total` == 5 |

Mock controller: a minimal `ControllerService` gRPC server started in-process that echoes back a fixed `InferResponse`. No real model needed.

---

## 8. Dependency on the controller being up

The generator cannot start an experiment without the controller. The design document for the controller is the prerequisite here. For Phase 1 development, the generator can be tested against a stub controller (see §7) before the real control plane is wired.

---

## 9. Open questions

1. **Prompt ordering across restarts.** If the generator is restarted mid-experiment, the round-robin counter resets. The log will show duplicate prompt sequences. Acceptable for MVP; worth noting in the experiment scripts.
2. **Backpressure signalling.** Currently, if the controller is slower than target RPS, the generator silently falls behind. Should it log a warning when actual RPS < 80% of target for > 10 s? Useful for diagnosing undersized workers.
3. **Per-prompt-type RPS control.** The scope doc says "fixed synthetic prompts at controlled RPS" — no per-type weighting mentioned. If the three types should be weighted differently (e.g. more reasoning prompts for Candidate B experiments), that's a future flag. For now, equal weight.
