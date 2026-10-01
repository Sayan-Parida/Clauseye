# Phase 7 CPU benchmark

Measurements for the Laya inference service, taken in the container on the
development host. Reproduce with:

```powershell
cd backend
docker compose --profile bench run --rm -e LAYA_THREADS=6 bench \
  --sizes 25,50,100 --concurrency 1,2,4 \
  --out /app/testdata/benchmark_cpu.json
```

Raw results: `backend/testdata/benchmark_cpu.json` and
`benchmark_cpu_scaling.json`.

## Host

| | |
|---|---|
| Platform | Linux 6.18 WSL2, x86_64 |
| vCPU | 12 (Docker Desktop) |
| torch | 2.14.0 (CPU build) |
| laya | 0.3.20 |
| GPU | none visible to torch |
| Checkpoint | `convaiinnovations/laya`, `english`, ModernBERT-large encoder (28 layers, hidden 1024) + 2-layer decision head, 842 MB |

CPU figures depend heavily on the host and on whether a container has a CPU
quota. The **ratios** between runs are the durable part; the absolute numbers are
specific to this machine.

## Model load

| Condition | Time |
|---|---|
| Cold (download checkpoint, ~842 MB) | **42.7 s** |
| Warm (checkpoint already in the volume) | **0.8 – 5.5 s** |

Load time is paid once per container start, not per request, because
`LAYA_PRELOAD=1` builds the checkpoint before uvicorn begins serving and
`/ready` returns 503 until it is resident. The compose healthcheck allows a
600 s `start_period` for the cold case.

## Per-clause latency and total analysis time

Batch size 8, `LAYA_THREADS=6`, 9 `noul` questions per clause.

| Clauses | Total | ms/clause | Throughput |
|---|---|---|---|
| 25 | 64.7 s | 2 589 | 0.386 cl/s |
| 50 | 131.4 s | 2 627 | 0.381 cl/s |
| 100 | 262.4 s | 2 624 | 0.381 cl/s |

**Latency is flat across 25, 50 and 100 clauses.** Cost is exactly linear in
clause count; batching buys nothing on CPU.

Projected end-to-end wall clock for a single request:

| Contract size | Time |
|---|---|
| 25 clauses | ~66 s |
| 50 clauses | ~131 s |
| 100 clauses | ~263 s (4.4 min) |

Observed end-to-end through the Go service adds a small overhead on top: a
4-clause request completed in 27.6 s including HTTP, PII redaction, aggregation
and explanation composition.

## Batch size has no effect on CPU

16 clauses:

| Batch size | ms/clause |
|---|---|
| 4 | **2 601** |
| 8 | 2 666 |
| 16 | 3 029 |
| 32 | 3 019 |
| 64 | 2 951 |

Small batches are marginally *faster*; large ones are about 15% slower, which is
consistent with the working set exceeding cache. Batching is therefore
correctness- and memory-motivated on CPU (it bounds peak memory and shares
tokenisation) but it is **not** a throughput lever. The default is 8.

## Threads do scale, sub-linearly, and peak around 6

16 clauses, one router per setting:

| torch threads | Total | ms/clause | Speed-up vs 1 |
|---|---|---|---|
| 1 | 136.0 s | 8 497 | 1.00× |
| 2 | 75.6 s | 4 724 | 1.80× |
| 4 | 58.6 s | 3 660 | 2.32× |
| 6 | 131.4 s (50-clause run) | 2 624 | 3.24× |
| 8 | 45.9 s | 2 868 | 2.96× |

Sub-linear, and **8 threads regresses**. Set `LAYA_THREADS` to roughly half the
logical core count, not to the core count.

> An earlier run on the Windows host showed no benefit from `LAYA_THREADS` at
> all. That was wrong: CPU sampling inside the container shows 605% of a core in
> use with 6 threads, so the scaling above is real and the Windows measurement
> was the artefact. This is why the benchmark runs in the container.

## Concurrency plateaus at two workers

16 clauses split across in-process workers, each with `LAYA_THREADS=6`:

| Workers | Wall | Aggregate throughput | CPU peak | RSS peak |
|---|---|---|---|---|
| 1 | 49.6 s | 0.322 cl/s | 605% | 3 071 MB |
| 2 | 38.3 s | 0.417 cl/s | 1 107% | 3 561 MB |
| 4 | 38.1 s | 0.420 cl/s | 1 139% | 3 629 MB |

Throughput improves 1.30× from one to two workers and then **stops**. CPU is
saturated at around 1 100%, i.e. roughly 11 of 12 logical cores. A third and
fourth worker buy nothing.

**Consequence for scaling out:** running more replicas on one machine will not
help, because the host is already saturated. Each replica loads its own 842 MB
copy of the weights (3.6 GB RSS with two workers against 3.0 GB with one), so
horizontal scaling consumes memory as fast as it consumes CPU.

## Resource envelope

| Metric | Value |
|---|---|
| Steady RSS, one worker | ~3.0 GB |
| RSS, two to four workers | ~3.6 GB |
| CPU, one worker | ~600% of a core |
| CPU, saturated | ~1 140% of a core |

The checkpoint alone is 842 MB; torch plus the activations account for the rest.
**A Laya container needs at least 4 GB of memory**, and the compose service sets
`deploy.resources.limits.memory: 4G`.

## GPU

No GPU was available, so no GPU figures exist. `scripts/benchmark_laya.py`
reports the GPU name and CUDA version automatically when torch can see a
device, so re-running on GPU hardware produces a comparable table with no code
change.

## What this means

1. **CPU-only Laya cannot serve interactive contract review.** A 50-clause
   contract takes just over two minutes; a 100-clause contract takes over four.
2. **The bottleneck is raw arithmetic, not overhead.** Batch size and
   concurrency both fail to move it, so there is no configuration of this
   service that makes it fast on this hardware.
3. **Thread count helps but saturates** at roughly half the logical cores, and
   yields about 3.2× over a single thread. That is the only tuning lever that
   does anything.
4. **Scaling out means scaling to new machines**, one model copy each. Memory
   per replica (≥3 GB) is a hard constraint on how many fit per host.
5. **A GPU is the obvious next variable** and is the single change most likely
   to change the order of magnitude. It has deliberately not been assumed here.
