# Znero International — Part 7 Load, Profiling & Bounded Soak Report (`docs/LOAD_TEST.md`)

**Target Environment**: Isolated Windows test host (`localhost`, single-node PostgreSQL 17 with `max_connections=100` / `synchronous_commit=on`, Kafka KRaft `127.0.0.1:9092`), 6 live separate Znero OS binaries (`znero.exe -role={api,smpp,dispatcher,reaper,webhook,poller}`, `DB_MAX_CONNS=12`, `RATE_LIMIT_MAX=0` for synthetic ceiling measurement) + local carrier simulators (`znero-smppmock.exe`, `znero-e2ehttpprov.exe`) against **`znero_test`**.

---

## 1. Benchmark Runs Executed

### Run A — 100,000-Message Mixed Burst + 5-Minute Continuous Mixed Soak (`scripts/run-part7-load.ps1 -SoakDuration 5m`)
- **Workload Mix**: `20,000` single `POST /api/v1/customer/sms` + `600 x 100` (`60,000`) `POST /api/v1/customer/sms/bulk` + `20,000` ESME SMPP `submit_sm` across 6 isolated customer orgs (3 HTTP upstream routes, 3 SMPP upstream routes).
- **Ingress Throughput & Errors**:
  - Accepted: **`100,000 / 100,000`** in **`421.64s`** (**`237.2 msg/s`**), **`errs=(single=0, bulk=0, smpp=0)`**.
  - Latencies: `http_single` `p50=178.66ms` / `p95=382.56ms` / `p99=633.11ms`; `http_bulk(100)` `p50=7387.87ms` / `p95=15058.28ms` / `p99=21575.14ms`; `smpp` `p50=52.64ms` / `p95=120.84ms` / `p99=168.57ms`.
- **Bounded Soak (`5m0s` / `304s` continuous mixed HTTP + SMPP @ `~25 msg/s`)**:
  - Total soak messages sent: **`7,356`**, **Errors: `0`**.
  - Stability telemetry (`t=0s` -> `t=304s`):
    - Active DB connections (`pg_stat_activity` for `znero_test`): steady **`41 -> 41`** (0 connection leak).
    - Goroutines (`pprof`): `api` **`42 -> 31`**, `dispatcher` **`13 -> 13`**, `smpp` **`14 -> 14`** (0 goroutine leak).
    - Process WorkingSet RSS: `znero-api` `87MB -> 86MB`, `znero-smpp` `108MB -> 49MB`, `znero-dispatcher` `37MB -> 41MB`, `znero-reaper` `21MB -> 22MB`, `znero-webhook` `20MB -> 20MB`, `znero-poller` `20MB -> 20MB` (flat, zero memory growth).

### Run B — Clean 60,000-Message Mixed Burst (Post-`BUG-052`/`BUG-053`/`BUG-054` Fixes) & Audit
- **Workload Mix**: `15,000` single HTTP + `300 x 100` (`30,000`) bulk HTTP + `15,000` ESME SMPP `submit_sm` (`60,000` total) on a freshly seeded `znero_test` (zero prior outbox backlog).
- **Ingress Throughput & Errors**:
  - Accepted: **`60,000 / 60,000`** in **`189.69s`** (**`316.3 msg/s`**, **+33.3% faster** than Run A after `BUG-052`), **`errs=(single=0, bulk=0, smpp=0)`**.
  - Latencies: `http_single` `p50=147.25ms` / `p95=334.24ms` / `p99=992.65ms`; `http_bulk(100)` `p50=5245.73ms` / `p95=20179.83ms` / `p99=23796.62ms`; `smpp` `p50=45.60ms` / `p95=115.35ms` / `p99=250.30ms`.
- **Async Carrier Drain & Honest Bottleneck Measurement**:
  - Using canonical statuses (`'accepted','submitting','submitted','unknown'`), `33,781 / 60,000` drained to terminal during the bounded `480s` drain window (`26,219` still draining cleanly at cutoff; **`~63.8 msg/s`** sustained E2E drain rate on single-node local PostgreSQL `synchronous_commit=on` with `8` dispatcher workers and per-message synchronous outbox/DLR transactions).
  - Post-load verification (`znero-loadgen.exe -mode=verify`): **all double-entry ledger transactions balanced**, **all wallet invariants (`available + held == cached`) intact**, and HTTP reconciliation `POST /api/v1/messaging/reconciliation/run` returned:
    `messages_checked=60001 discrepancy_count=0 ledger_matches=true`.

---

## 2. Live `pprof` Findings & Optimizations Discovered

Captured via loopback-only `http://127.0.0.1:6060..6062/debug/pprof/{profile?seconds=8,heap}` into `.tmp/pprof/*.pb.gz`:

1. **Before `BUG-052` Fix**:
   - `api_cpu.pb.gz` (`8.41s` samples): **`88.11%` (`7.41s`)** spent inside `logx.contextHandler -> slog.(*TextHandler).Handle -> os.(*File).Write` due to unbuffered Windows stdout writes on every `outbound message accepted` (logged 100x per `/sms/bulk` call) + `AccessLog`. Combined with concurrency contention, 56 `http_bulk(100)` requests exceeded `loadgen`'s initial `15s` client timeout in an earlier aborted run (`BUG-052`).
   - `smpp_cpu.pb.gz` (`8.74s` samples): **`81.92%` (`7.16s`)** spent in `messaging.(*Service).Submit -> slog.Info("outbound message accepted") -> os.(*File).Write`.
2. **After `BUG-052` Fix** (demoted the 3 per-SMS hot-path logs in `messaging/service.go`, `dispatcher.go`, and `dlr.go` from `Info` to `Debug`):
   - `smpp_cpu.pb.gz` (`7.73s` samples): slog hotspot dropped from **`81.92% -> 0%`**; dominant work became genuine DB work (`Service.Submit` `70.76%`, `pgx.ExecPrepared` `29.11%`).
   - `api_cpu.pb.gz` (`10.77s` samples): `FD.Write` dropped from **`88.11% -> 51.25%`** (HTTP `AccessLog`), raising mixed ingress speed from **`237.2 -> 316.3 msg/s`**.
   - **Heap profiles**: `api_heap.pb.gz` **`7.13 MB`** total live `inuse_space`; `dispatcher_heap.pb.gz` **`7.73 MB`** total live `inuse_space` (`pgx` row scan buffers + `phonenumbers` metadata); `smpp_heap.pb.gz` **`257.51 MB`** transient Argon2id (`m=64MB`) memory during simultaneous ESME initial binds (`handleBind`), reclaimed cleanly by GC afterwards (process RSS stabilized at `49 MB`).
3. **Additional Bottlenecks Found & Logged**:
   - **`BUG-051`**: Fixed SMPP upstream bind stampede on cold start in `smpp.ConnectionManager.GetSession` via per-connection singleflight locking (`bindLocks`).
   - **`BUG-053`**: Fixed `ReconciliationService.RunReconciliation` false `ledger_totals_mismatch` when an `on_provider_accept` message transitions to `'unknown'` after an ambiguous DLR (`TestReconciliation_OnProviderAcceptUnknownDLR`).
   - **`BUG-054`**: Fixed stale non-terminal status filter in `cmd/loadgen/main.go`.
   - **`POST /api/v1/customer/sms/bulk` & Dispatcher Ceiling**: Sequential per-item transaction commits cap single-node Windows Postgres ingestion/drain (~`316 msg/s` burst, `~64 msg/s` full E2E dispatch+DLR+ledger drain). Horizontal scaling of stateless `znero-dispatcher` / `znero-api` processes and/or batched outbox leasing + async buffered logging remain clean production optimization levers.
