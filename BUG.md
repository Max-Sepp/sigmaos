# Bug: fresh `BootMinNode` capacity races with service registration visibility

## Summary

Hotel benchmark jobs (`benchmarks/hotel_test.go`'s `HotelJobInstance`) fail
intermittently with `file not found` panics when `BootMinNode` is used to add
scheduling capacity right before starting the job (the pattern
`TestHotelDevSigmaosSearchScaleCache` already uses, and that
`benchmarks/hotel_eviction_bench_test.go` -- added on this branch -- follows
too). A service that gets spawned onto one of the freshly-booted nodes
registers itself (posts a leased endpoint file) successfully, but a
*different* client (the test process, on a different node) can fail to see
that registration -- sometimes for a few hundred ms, sometimes for longer
than any reasonable retry budget.

This reproduces on completely unmodified code: `TestHotelDevSigmaosSearchScaleCache`
(`benchmarks/benchmarks_test.go`), which nothing in this session touched,
fails the same way.

**Status: confirmed (2026-07-20).** Re-ran the exact repro below against
unmodified `master` (`benchmarks/benchmarks_test.go` has zero diff from
`master`) and it failed in 8.72s with the identical signature: `wwwd`'s
`WaitStart` returned success at `14:53:55.400425`, and the test's own read of
`http-addr` failed with "file not found" 1.6ms later at `14:53:55.402003`,
cascading into the same nil-`*WebClnt` panic in `hotel.WebClnt.request`
(`apps/hotel/clnt.go:47`). This is a real, reproducible, pre-existing bug, not
an artifact of this branch or this environment. See "Root cause, corrected"
below for a revision to the original mechanism hypothesis.

## Reproduction

```
./stop.sh --parallel --nopurge --skipdb
go clean -testcache
go test -v sigmaos/benchmarks --start --run TestHotelDevSigmaosSearchScaleCache --timeout 300s
```

Also reproduces via the new `benchmarks/hotel_eviction_bench_test.go` added on
this branch (`urop/hotel-eviction-baseline`):

```
./stop.sh --parallel --nopurge --skipdb
go clean -testcache
go test -v sigmaos/benchmarks --start --run TestHotelEvictionRandom --timeout 300s
```

Both use `mrts.GetRoot().BootMinNode(3)` before starting a hotel job with a
scaled-up cache (`CacheCfg.NSrv > 1`).

## Evidence

### Failure 1: `epcache` not found

```
panic: runtime error: invalid memory address or nil pointer dereference
sigmaos/apps/hotel.NewWebClnt(...)
sigmaos/benchmarks_test.NewHotelJob(...)
	.../benchmarks/hotel_test.go:182
Error:  &serr.Err{ErrCode:0xd, Obj:"epcache", ...}
Messages: Error NewHotelJob: {Err: "file not found" Obj: "epcache" ()}
```

Debug log (`SIGMADEBUG=TEST;ALWAYS;ERROR;SPAWN_LAT;EPCACHE;...;BOOT;KERNEL;`),
timestamps in order:

```
14:15:56.105677  BOOT  Start: sigma-c317 nodetype minnode          <- new min-node begins booting
14:15:56.144432  PROCCLNT  Spawn epcached-...                       <- epcached spawn issued
14:15:56.160816  PROCCLNT  ...spawned on msched sigma-c317          <- scheduler picked the brand-new node
14:15:56.498055  PROCCLNT  WaitStart done epcached-...               <- Started() fired, 392ms after node boot began
14:15:56.498464  TEST  Err NewClnt: file not found "epcache"         <- 0.4ms later, read fails
```

`sigma-c317` (a `minnode`: `msched;ux;s3;chunkd`, per `start-kernel.sh`'s
`--boot minnode` case) started booting at `.105677`. The scheduler picked it
for `epcached` at `.160816` -- 55ms after it started booting. `WaitStart`
(which unblocks once `epcached` calls `Started()`) returned success at
`.498055`. The very next statement in the test process -- reading the
`name/epcache` endpoint file `epcached` should have just registered -- failed
with not-found **0.4ms later**.

### Ordering is not the bug (traced, confirmed)

Traced `epcached`'s startup path to rule out an app-level ordering bug:

- `apps/epcache/srv/srv.go:83` `sigmasrv.NewSigmaSrv(epcache.EPCACHE, ...)`
  -> `sigmasrv/memfssrv/makesrv.go:60` `srv.PostMount(sc, srvpath)`
  (`sigmasrv/memfssrv/sigmapsrv/sigmapsrv.go:81`) -- creates the leased
  `name/epcache` endpoint file. This is synchronous and completes *before*
  `NewSigmaSrv` returns.
- Only afterward does `RunSrv()` call `ssrv.RunServer()` -> `Serve()`
  (`sigmasrv/sigmasrv.go:234`), which calls `sc.Started()` -- the thing that
  unblocks the test's `WaitStart`.

So the registration write is guaranteed to have already happened by the time
`Started()` fires. The two clients involved are genuinely different
`SigmaClnt`/`FsLib` instances on different nodes (`epcached`'s own client,
running on the brand-new min-node; the test process's client, on a different,
long-running node).

### Root cause, corrected: this is not `named`/etcd staleness

The paragraph above (in the original version of this doc) speculated that the
failure was "a propagation/visibility gap ... through the distributed
named/lease layer," i.e. that `named` behaves like a multi-replica,
eventually-consistent store. Traced further (2026-07-20) and **that framing is
wrong**:

- `named` is a single, leader-elected process per realm (`namesrv/named.go`,
  leader election via `leaderetcd`); it is not a replicated service with
  independent stale readers. `namesrv/fsetcd/dir.go`'s `Create` performs the
  etcd `Txn` *and* updates the in-memory directory cache
  (`namesrv/fsetcd/dcache.go`) synchronously, under the same lock the read
  path (`dir.go`'s `readDir`/`Lookup`) uses. Once `PostMount`'s RPC returns
  success, any subsequent read through that same `named` -- from any node --
  should see it immediately. There is no second `named` replica in the picture
  that could be lagging.
- There is also no client-side negative/not-found caching in
  `fslib`/`pathclnt` (`sigmaclnt/fsclnt/pathclnt/*.go`) that could explain a
  stale "not found" on a fresh Walk RPC.

**The actual gap is one level up, in node/capacity readiness, and is already
a known, named pattern in this codebase that the hotel benchmarks simply
don't use:**

- `test/test.go:238` `Tstate.BootMinNode` (what the hotel benchmarks call, via
  `mrts.GetRoot().BootMinNode(N)`, `benchmarks/benchmarks_test.go:325,1155`
  and `benchmarks/hotel_eviction_bench_test.go:158`) returns as soon as the
  new kernel container is dialable (`bootclnt.NewKernelClntStart`). It does
  **not** wait for the new node's `msched`/realm-facing state to be fully up.
- `test/realmtest.go:85` `RealmTstate.bootNode(n, waitForNamed)` already
  exists specifically to close a version of this gap: its comment (line
  ~89-91) says "We may need to wait for the realm's new named to come up
  during booting the node so that the realm's UX and S3 can register
  themselves," and it retries `GetDir` on the realm's named root
  (`retry.RetryAtMostOnce`, line 109) until it's up before returning. This is
  direct evidence the codebase already recognizes "boot returns before the
  new node's realm-facing registration exists" as a known race class -- but
  the hotel benchmarks call `Tstate.BootMinNode`, not this realm-aware
  variant, so they get none of that protection.
- Compounding this: `fslib`'s endpoint reads (`sigmaclnt/fslib/mount.go`'s
  `ReadEndpoint`/`GetFile`) have **zero retry-on-ENOENT** anywhere in the
  path. So any transient cold-start lag -- of whatever origin -- surfaces
  instantly as a hard, fatal "file not found" instead of being absorbed.

Failure 3 (retry budget of ~6s exceeded) is therefore more likely genuine
scheduling/cold-start contention under concurrent multi-service,
multi-min-node startup than indefinite staleness -- consistent with the
"suggested follow-ups" below, which already flagged this as the more probable
explanation before this correction.

### Failure 2: `http-addr` not found (same shape, next call site)

After patching the `epcache` read with a short bounded retry
(`retry.RetryNotFound`, see "What was tried" below), the identical failure
shape recurred one step later in the same startup sequence:

```
Error: &serr.Err{ErrCode:0xd, Obj:"http-addr", ...}
Messages: Err NewWebClnt: {Err: "file not found" Obj: "http-addr" ()}
```

`apps/hotel/clnt.go:27` `hotel.NewWebClnt` -> `apps/hotel/job.go:142`
`GetJobHTTPAddrs` -> `fsl.ReadEndpoint(JobHTTPAddrsPath(job))`, reading the
file `hotel-wwwd` posts via `www.MkEndpointFile(...)`
(`apps/hotel/wwwd.go:140`) once it starts. Same shape: `wwwd` may have been
scheduled onto a freshly-booted min-node too.

### Failure 3: retry budget exhausted, not just a brief race

After generalizing the fix into a shared retry helper
(`retry.RetryNotFound[T any]`, bounded by
`sp.Conf.Path.RESOLVE_TIMEOUT * sp.Conf.Path.MAX_RESOLVE_RETRY` = `200ms * 30`
= ~6s, per `sigmap/hyperparams.go:37-38`) and applying it to both the
`epcache` and `http-addr` reads, both of those specific "not found" errors
disappeared from the logs entirely -- but the test still failed, this time
with the retry helper itself giving up:

```
Error: &serr.Err{ErrCode:0x14, Obj:"RetryNotFound", ...}
Messages: Err NewWebClnt: {Err: "Unreachable" Obj: "RetryNotFound" (<nil>)}
```

This means `wwwd`'s registration didn't become visible within ~6 seconds at
all in this run -- not a few-hundred-ms propagation lag, but either genuinely
slow cold-start (multiple services concurrently cold-starting across several
freshly-booted min-nodes, plus 3 cache replicas, all contending for the same
newly-added capacity) or a separate, more serious issue in the same family as
the `lcsched`/`msched` scheduler deadlock found on `urop/mr-straggler-baseline`
(see that branch's history / `BUG_repro.txt`) -- not yet distinguished.

## What was tried (reverted, not merged)

1. **Localized fix**: wrapped the single `epcache` read
   (`apps/epcache/srv/job.go`'s `NewEPCacheJob`) in a bounded retry
   (`retry.RetryDefDur` + a `serr.IsErrorNotfound`-based `okf`, mirroring the
   existing precedent in `rpc/clnt/cache/cache.go`'s `RPCRetryNotFound` and
   `test/realmtest.go:109`'s `retry.RetryAtMostOnce` used for the near-identical
   "wait for named to catch up after booting a node" problem). This cleared
   the `epcache` failure specifically, exposing failure 2.
2. **Generalized fix**: added `retry.RetryNotFound[T any](f func() (T, error))
   (T, error)` to `util/retry/retry.go` alongside the existing
   `RetryAtMostOnce`/`RetryAtLeastOnce` idioms, and switched both the
   `epcache` (`apps/epcache/srv/job.go`) and `http-addr`
   (`apps/hotel/job.go`'s `GetJobHTTPAddrs`) reads to use it. This cleared
   both "not found" failures, exposing failure 3.

Both changes were reverted (not kept on this branch) once failure 3 surfaced,
since the fix had stopped being a small, targeted patch and turned into
open-ended investigation of real cold-start latency / possible scheduler
contention -- out of scope for `urop/hotel-eviction-baseline`, whose mandate
is a zero/near-zero production-code-footprint benchmark addition.

## Suggested follow-ups

- Determine whether failure 3 is pure cold-start latency (in which case
  raising `RESOLVE_TIMEOUT`/`MAX_RESOLVE_RETRY`, or giving `RetryNotFound`
  callers a longer, call-site-specific budget, is sufficient) or a genuine
  scheduling stall (in which case it may be related to the `lcsched`/`msched`
  deadlock documented on `urop/mr-straggler-baseline`). A `SIGQUIT`
  goroutine dump of the test process and every kernel container during a
  failing run (same technique used to diagnose that deadlock) would settle
  this quickly.
- If it is cold-start latency: consider whether `BootMinNode`/`BootNode`
  should block until the new node's capacity is actually usable (e.g., a
  round-trip health check through the new node's proxies) rather than
  returning as soon as the kernel container itself is reachable.
- If a generic retry-on-not-found helper is wanted, `retry.RetryNotFound[T
  any]` (as prototyped and reverted here) is a reasonable, low-risk shape,
  but the two known call sites (`apps/epcache/srv/job.go`,
  `apps/hotel/job.go`) are very unlikely to be the only two racy reads of a
  freshly-registered file in the hotel job's startup path -- expect more
  (e.g. `hotel-geod`'s registration, cache replica endpoints) to surface once
  the first `MAX_RESOLVE_RETRY`-exceeding case is actually fixed.
