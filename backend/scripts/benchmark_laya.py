"""End-to-end CPU benchmark for the Laya inference service.

This exists because the deployment decision in Phase 8 has to be made on
measurements, not on the intuition that "a transformer on CPU is probably fine".
The preliminary numbers in docs/benchmark.md suggest it is not fine.

What it measures, per the Phase 7 brief:

  - model start-up and checkpoint load time (cold and warm)
  - per-clause latency
  - total analysis time for 25, 50 and 100 clauses
  - throughput in clauses per second
  - peak and mean CPU, and resident memory, of this process
  - behaviour under concurrent requests

GPU is reported if and only if torch can see a device, so the same script works
unchanged once a GPU is introduced. Nothing here assumes one.

Caveat on reading the numbers: CPU figures depend heavily on the host and on
whether the process is containerised with a CPU quota. The absolute values below
are specific to the machine named in the output; the ratios between runs are the
durable part.

Usage:
    python scripts/benchmark_laya.py
    python scripts/benchmark_laya.py --sizes 25,50 --concurrency 1,2
"""

from __future__ import annotations

import argparse
import gc
import json
import os
import platform
import statistics
import sys
import threading
import time
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from laya_questions import QUESTION_SETS  # noqa: E402


# ------------------------------------------------------------------ test data

def load_clauses(count: int) -> list[str]:
    """Build `count` analysis windows from the golden set.

    Real clause text is used rather than synthetic padding, because token length
    is what determines the cost and padding would flatter the result.
    """
    golden = json.loads((REPO / "testdata" / "clauses_golden.json").read_text(encoding="utf-8"))
    base = [c["clause"] for c in golden["clauses"]]
    if not base:
        raise SystemExit("no clauses in testdata/clauses_golden.json")
    out = [base[i % len(base)] for i in range(count)]
    return out


def strip_numbering(text: str) -> str:
    import re

    return re.sub(
        r"(?i)^\s*(?:(?:section|article|clause)\s+\d+|\d+(?:\.\d+)*\.?|\([a-z0-9]{1,4}\)|[ivx]+\.)\s+",
        "",
        text,
    ).strip()


# ------------------------------------------------------------- instrumentation

class ResourceSampler:
    """Samples this process's CPU and resident memory on a background thread.

    psutil is not a dependency, so the numbers come from /proc, which is
    available in the linux container this normally runs in and on Windows via the
    same API being absent -- in which case the sampler reports nothing rather than
    failing the run.
    """

    def __init__(self, interval: float = 0.5) -> None:
        self.interval = interval
        self.cpu_samples: list[float] = []
        self.rss_samples_kb: list[int] = []
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None

    @staticmethod
    def _read_rss_kb() -> int | None:
        try:
            with open("/proc/self/status", encoding="utf-8") as handle:
                for line in handle:
                    if line.startswith("VmRSS:"):
                        return int(line.split()[1])
        except OSError:
            pass
        return None

    @staticmethod
    def _read_cpu_seconds() -> float | None:
        try:
            with open("/proc/self/stat", encoding="utf-8") as handle:
                fields = handle.read().rsplit(") ", 1)[1].split()
            # utime and stime are fields 14 and 15 (1-indexed), which is 11 and
            # 12 after the comm field is removed above.
            return (int(fields[11]) + int(fields[12])) / os.sysconf("SC_CLK_TCK")
        except (OSError, IndexError, ValueError):
            pass
        return None

    def _run(self) -> None:
        previous = self._read_cpu_seconds()
        previous_at = time.monotonic()
        while not self._stop.wait(self.interval):
            current = self._read_cpu_seconds()
            now = time.monotonic()
            if current is not None and previous is not None and now > previous_at:
                # Percent of one core over the interval.
                self.cpu_samples.append(
                    100.0 * (current - previous) / (now - previous_at)
                )
                previous, previous_at = current, now
            rss = self._read_rss_kb()
            if rss is not None:
                self.rss_samples_kb.append(rss)

    def __enter__(self) -> "ResourceSampler":
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()
        return self

    def __exit__(self, *_exc: object) -> None:
        self._stop.set()
        if self._thread is not None:
            self._thread.join(timeout=3)

    def report(self) -> dict[str, float | None]:
        def stat(values: list[float]) -> float | None:
            return round(max(values), 1) if values else None

        mean_rss = (
            round(statistics.mean(self.rss_samples_kb) / 1024, 1)
            if self.rss_samples_kb
            else None
        )
        peak_rss = (
            round(max(self.rss_samples_kb) / 1024, 1) if self.rss_samples_kb else None
        )
        return {
            "cpu_percent_peak": stat(self.cpu_samples),
            "cpu_percent_mean": round(statistics.mean(self.cpu_samples), 1) if self.cpu_samples else None,
            "rss_mb_peak": peak_rss,
            "rss_mb_mean": mean_rss,
            "samples": len(self.cpu_samples),
        }


# ------------------------------------------------------------------ benchmarks

def build_router(model: str, device: str | None, threads: str | None):
    import laya

    kwargs: dict = {"preload": False, "max_loaded": 1}
    if device:
        kwargs["device"] = device
    router = laya.Router(**kwargs)

    applied_threads = None
    if threads:
        try:
            import torch

            torch.set_num_threads(int(threads))
            applied_threads = int(threads)
        except (ImportError, ValueError):
            pass

    start = time.monotonic()
    router.preload([model])
    return router, time.monotonic() - start, applied_threads


def run_batch(router, model: str, states: list[str], batch_size: int, question_set: str = "default"):
    questions = QUESTION_SETS[question_set]()
    requests = [{"state": s, "questions": questions, "model": model} for s in states]
    return router.predict_batch(requests, batch_size=batch_size)


def bench_sizes(router, model: str, sizes: list[int], batch_size: int, repeats: int) -> list[dict]:
    rows: list[dict] = []
    for count in sizes:
        clauses = [strip_numbering(c) for c in load_clauses(count)]
        for attempt in range(1, repeats + 1):
            gc.collect()
            with ResourceSampler() as sampler:
                start = time.monotonic()
                results = run_batch(router, model, clauses, batch_size)
                elapsed = time.monotonic() - start
            per_clause = elapsed / count * 1000
            row = {
                "clauses": count,
                "attempt": attempt,
                "batch_size": batch_size,
                "results": len(results),
                "total_seconds": round(elapsed, 2),
                "ms_per_clause": round(per_clause, 1),
                "clauses_per_second": round(count / elapsed, 3),
                **sampler.report(),
            }
            rows.append(row)
            print(
                f"  n={count:<4} run {attempt}: {elapsed:7.2f}s total  "
                f"{per_clause:7.1f} ms/clause  {row['clauses_per_second']:.3f} cl/s  "
                f"cpu_peak={row['cpu_percent_peak']}%  rss_peak={row['rss_mb_peak']} MB"
            )
    return rows


def bench_batch_sizes(router, model: str, clauses: list[str], batch_sizes: list[int]) -> list[dict]:
    rows: list[dict] = []
    for batch_size in batch_sizes:
        gc.collect()
        start = time.monotonic()
        run_batch(router, model, clauses, batch_size)
        elapsed = time.monotonic() - start
        rows.append({
            "batch_size": batch_size,
            "total_seconds": round(elapsed, 2),
            "ms_per_clause": round(elapsed / len(clauses) * 1000, 1),
        })
        print(f"  batch_size={batch_size:<4}: {elapsed:7.2f}s  {rows[-1]['ms_per_clause']:7.1f} ms/clause")
    return rows


def bench_threads(model: str, device: str | None, clauses: list[str], thread_counts: list[int], batch_size: int) -> list[dict]:
    rows: list[dict] = []
    for threads in thread_counts:
        router, load_seconds, applied = build_router(model, device, str(threads))
        gc.collect()
        start = time.monotonic()
        run_batch(router, model, clauses, batch_size)
        elapsed = time.monotonic() - start
        rows.append({
            "threads": applied,
            "load_seconds": round(load_seconds, 2),
            "total_seconds": round(elapsed, 2),
            "ms_per_clause": round(elapsed / len(clauses) * 1000, 1),
        })
        print(f"  threads={str(applied):<5}: load {load_seconds:5.2f}s  {elapsed:7.2f}s  {rows[-1]['ms_per_clause']:7.1f} ms/clause")
        del router
        gc.collect()
    return rows


def bench_concurrency(router, model: str, clauses: list[str], batch_size: int, workers: int) -> dict:
    """Run `workers` batches at once and report aggregate throughput.

    If CPU inference is memory-bandwidth bound, aggregate throughput will fall as
    workers rise rather than staying flat. That distinction decides whether
    horizontal scaling on CPU is viable at all.
    """
    per_worker = max(1, len(clauses) // workers)
    shards = [clauses[i * per_worker : (i + 1) * per_worker] for i in range(workers)]
    shards = [s for s in shards if s]
    results: list[dict] = []
    barrier = threading.Barrier(len(shards))
    latencies: list[float] = []
    lock = threading.Lock()

    def worker(shard: list[str]) -> None:
        barrier.wait()
        started = time.monotonic()
        run_batch(router, model, shard, batch_size)
        elapsed = time.monotonic() - started
        with lock:
            latencies.append(elapsed)

    with ResourceSampler() as sampler:
        start = time.monotonic()
        threads = [threading.Thread(target=worker, args=(shard,)) for shard in shards]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        wall = time.monotonic() - start

    total_clauses = sum(len(s) for s in shards)
    results = {
        "workers": len(shards),
        "clauses": total_clauses,
        "wall_seconds": round(wall, 2),
        "aggregate_clauses_per_second": round(total_clauses / wall, 3),
        "slowest_worker_seconds": round(max(latencies), 2),
        **sampler.report(),
    }
    print(
        f"  workers={len(shards):<3}: {wall:7.2f}s wall for {total_clauses} clauses  "
        f"aggregate {results['aggregate_clauses_per_second']:.3f} cl/s  "
        f"cpu_peak={results['cpu_percent_peak']}%  rss_peak={results['rss_mb_peak']} MB"
    )
    return results


# ------------------------------------------------------------------------ main


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model", default=os.getenv("LAYA_MODEL", "english"))
    parser.add_argument("--device", default=os.getenv("LAYA_DEVICE") or None)
    parser.add_argument("--batch-size", type=int, default=int(os.getenv("LAYA_BATCH_SIZE", "8")))
    parser.add_argument("--threads", default=os.getenv("LAYA_THREADS") or None)
    parser.add_argument("--sizes", default="25,50,100")
    parser.add_argument("--repeats", type=int, default=1)
    parser.add_argument("--skip-batch-sweep", action="store_true")
    parser.add_argument("--skip-thread-sweep", action="store_true")
    parser.add_argument("--skip-concurrency", action="store_true")
    parser.add_argument("--concurrency", default="1,2,4")
    parser.add_argument("--out", default=None, help="write the full result set as JSON")
    args = parser.parse_args()

    sizes = [int(s) for s in args.sizes.split(",") if s]
    worker_counts = [int(c) for c in args.concurrency.split(",") if c]

    report: dict = {
        "host": {
            "platform": platform.platform(),
            "python": platform.python_version(),
            "cpu_count": os.cpu_count(),
            "threads_configured": args.threads,
        }
    }

    print("=" * 78)
    print("Laya CPU benchmark")
    print("=" * 78)
    print(f"host: {report['host']['platform']}")
    print(f"cpus: {report['host']['cpu_count']}  threads configured: {args.threads}")

    import torch

    report["torch_version"] = torch.__version__
    report["cuda_available"] = torch.cuda.is_available()
    if report["cuda_available"]:
        report["gpu"] = torch.cuda.get_device_name(0)
        print(f"GPU: {report['gpu']}  (CUDA {torch.version.cuda})")
    else:
        print("GPU: none visible to torch (CPU build)")

    import laya

    report["laya_version"] = laya.__version__
    print(f"laya: {laya.__version__}  torch: {torch.__version__}")
    print()

    print("loading checkpoint...")
    router, load_seconds, applied_threads = build_router(args.model, args.device, args.threads)
    report["load_seconds"] = round(load_seconds, 2)
    report["threads_applied"] = applied_threads
    print(f"checkpoint load: {load_seconds:.2f}s  threads applied: {applied_threads}")
    print()

    print("warm-up run (excluded from results)...")
    run_batch(router, args.model, [strip_numbering(c) for c in load_clauses(4)], args.batch_size)
    print()

    print(f"clause counts {sizes}, batch size {args.batch_size}")
    report["sizes"] = bench_sizes(router, args.model, sizes, args.batch_size, args.repeats)
    print()

    probe = [strip_numbering(c) for c in load_clauses(16)]

    if not args.skip_batch_sweep:
        print("batch size sweep (16 clauses)")
        report["batch_sweep"] = bench_batch_sizes(router, args.model, probe, [4, 8, 16, 32, 64])
        print()

    if not args.skip_thread_sweep:
        print("thread sweep (16 clauses, one router per setting)")
        report["thread_sweep"] = bench_threads(
            args.model, args.device, probe, [1, 2, 4, 8], args.batch_size
        )
        # Rebuild at the requested setting for the remaining benchmarks.
        router, load_seconds, applied_threads = build_router(args.model, args.device, args.threads)
        print()

    if not args.skip_concurrency:
        print(f"concurrency (16 clauses split across {worker_counts} in-process workers)")
        report["concurrency"] = [
            bench_concurrency(router, args.model, probe, args.batch_size, w)
            for w in worker_counts
        ]
        print()

    if args.out:
        out = Path(args.out)
        out.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        print(f"full results written to {out}")

    slowest = max((r["ms_per_clause"] for r in report["sizes"]), default=0)
    print("=" * 78)
    print(f"worst per-clause latency: {slowest:.0f} ms")
    for size in sizes:
        rate = slowest * size / 1000
        print(f"  a {size}-clause contract therefore takes about {rate:.0f} s on this host")
    print("=" * 78)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
