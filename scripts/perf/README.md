# Linux performance measurements

This harness builds isolated copies of the application and uses synthetic databases,
local pages, and a test-only Unix socket connected to the actual Wails/WebKitGTK
window. The observation hook is copied into the isolated build; normal application
builds have no observation endpoint. Never point the harness at real user data.
Each dedicated Xvfb starts with its screen saver disabled, so a long synthetic
operation cannot activate the X server's idle screen saver while waiting for frames.

The accepted successor goal in `.codex/goals/2026-11-10-linux-performance-v2.md`
defines the current acceptance criteria; the original goal and failed large-baseline
runs remain historical evidence. `linux_perf.py` records source and fixture hashes,
commands, raw samples, correctness assertions, and process resources. Comparable
scenarios use identical fixtures and settings on both sides.

Requirements: Linux amd64, Xvfb, Chromium, GTK 3 and WebKitGTK 4.1 development
libraries, Node/npm, Python 3.10+ with `websocket-client`, and Go's toolchain
download support. The runner pins Go 1.24.13 and Wails 2.12.0. Both native proxy
executables must exist under the source's `bin/linux-amd64/`. Supply the verified
Mihomo executable separately; the runner records all three hashes.

```sh
export LINUXPERF_MIHOMO_BINARY=/absolute/path/to/verified/mihomo
export LINUXPERF_PRESSURE_REFERENCE=/absolute/path/to/portable-pressure/reference.json
python3 scripts/perf/linux_perf.py measure --role baseline --root "$PERF_BASELINE_ROOT" --output "$PERF_EVIDENCE_ROOT/before"
python3 scripts/perf/linux_perf.py measure --role candidate --root "$PERF_CANDIDATE_ROOT" --output "$PERF_EVIDENCE_ROOT/after"
python3 scripts/perf/linux_perf.py compare --before "$PERF_EVIDENCE_ROOT/before" --after "$PERF_EVIDENCE_ROOT/after" --output "$PERF_EVIDENCE_ROOT/comparison"
python3 -m unittest discover -s scripts/perf/tests -v
```

Every output path must be new. Keep the source snapshots unchanged after a run;
comparison verifies their current contents against the recorded manifest. Build
inputs are copied using a whitelist, excluding personal root configuration and
runtime directories before any copying. The source roots remain read-only.

Each v2 side includes 100/1,000-entry GUI catalogs, 0/1/5/10 real Chromium
instances, local Xray/sing-box/Mihomo traffic, file downloads/archives, logging,
and 32 registered Go microbenchmarks. The baseline role also revalidates a pinned,
portable reference to the approved old 10,000-entry timeout. The candidate role
must complete the full 10,000-entry GUI catalog and ten additional resource rounds.
The pressure exception applies only to that old catalog branch. Failed or missing
candidate work cannot inherit it. Omitting `--role` preserves the original full
symmetric matrix; original and v2 roles cannot be mixed in a comparison.

GUI latency starts on the host before dispatch, includes entry queueing, and ends
when the host receives the verified business result after two animation frames.
The calibrated test-only DOM entry is identical on both sides. Each API is timed
as a separate host request. Keyword save visibility and modal closure are phases
of one save action, timed from the same host origin. Instance actions click the
real UI; CDP independently verifies the local page's title, URL and contents.
Cancellation, event freshness and persistence-error probes run outside timing loops.

Rendered proof version 2 records both the native `requestAnimationFrame` timestamp
and `performance.now()` inside every callback in the final successful predicate
window. Two distinct native timestamps are required. Callback execution times may
tie because clocks can be coarsened, but must never decrease. A repeated native
timestamp waits for the first distinct timestamp under the original deadline;
nonfinite, negative or decreasing clocks fail. The predicate must hold before and
after these callbacks. Native frame timestamps are not ordered against predicate
execution times. All additional waiting and any earlier failed predicate windows
remain inside the original host timing. Raw receipt validation requires the version,
complete final-window callback sequence and deadline; old proof records cannot be
upgraded by inventing native timestamps. A method change requires fresh affected
baseline and candidate measurements, with historical failures retained separately.

Short operations have 30 samples. Startup/resources use 10 independent fresh
process rounds, with 15 seconds of warmup and 30 seconds of resource sampling.
Large baseline catalogs may take many minutes per startup; the observation
timeout is 30 minutes. The backend's own timeout behavior is unchanged. A
`--smoke` run reduces samples for diagnostics and is always rejected by compare.

Resource CPU is read at the kernel's clock-tick resolution; PSS/RSS and open file
descriptors come from `/proc`. Owned processes are identified by PID and start
time, including after reparenting. Reparented Chromium helpers are discovered
only for the current user and exact isolated runtime path. The v2 baseline records
60 lifecycle files covering 62 application launches; the candidate records 70 files
covering 73 launches. These include all persistence restarts. Every launch must exit with status zero without forced
shutdown, surviving owned processes, or zombies; comparison revalidates the raw
lifecycle records. Missing resource evidence from a live process is an error.
Cleanup signals use Linux pidfds, through Python or libc, and recheck the
recorded process start time before signalling.
Raw resource windows contain 31 observations across at least 30 seconds after
15 seconds of warmup. One-second observations use absolute monotonic deadlines.
CPU is scaled to a 30-second equivalent using the actual observation span;
the raw cumulative CPU delta and actual span are retained. This prevents collection
overhead from becoming an artificial CPU regression. Every launched browser's
PID/start identity must remain live with positive PSS/RSS at every point.
The comparator recomputes every registered summary from
these raw points and checks each window against its process lifecycle. Candidate
large-catalog resource distributions include goroutines and each process category's
CPU, PSS, RSS, FD count, process count, observed PSS/RSS peaks, and a sum of
per-process `VmHWM` values. The latter is labelled an upper bound: individual
high-water marks need not occur simultaneously. One-second observations do not
claim to capture every transient memory peak.
A near-zero CPU floor of two accounting ticks
and a PSS floor of one 4 KiB page apply only when both medians are within that
floor. Large metrics retain the 5% regression limit. The independent median
bootstrap uses 4,000 resamples, seed 1729 and a 95% confidence interval; an
uncertain result fails acceptance. Microbenchmarks remain auxiliary and cannot
establish a user-flow improvement.
The v2 comparison reports candidate-only large-catalog medians, p95 and ranges.
It does not invent a zero baseline, percentage, confidence interval, stable gain,
or 5% regression decision for that branch. All paired core metrics retain the
original strict decision rule. Candidate-only correctness and completion remain
required even when the paired results improve.

The evidence validator requires explicit test, metric, command and correctness
registries, complete producer lifecycles, positive sample counts, fresh hashed
artifacts, and successful exit statuses. Calibration covers failure records,
benign log words, fragmented Go JSON, missing work and ambiguous statistics.
Do not edit the fixtures or run competing builds/benchmarks during measurement.
