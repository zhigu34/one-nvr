# M1-B execution rulings

Chronological author rulings and their cost if wrong. Acceptance remains pending until the final exact-candidate CI gates pass.

Ruling: Keep the user's established shared checkout, using a new development branch and explicit commits rather than a second checkout — previous inline development and existing dependencies are here; no main edits — cost if wrong: concurrent manual checkout changes would need reconciliation.

Ruling: Native isolated PostgreSQL/Go are used for RED→GREEN because this host has no Docker daemon; the same tests run in pinned Docker CI before acceptance — cost if wrong: platform-specific issues remain until CI.

Ruling: The plan's shorthand signatures use context.Context/auth.Principal and explicit DTOs; define shared types as their first actual consumer arrives instead of prebuilding empty endpoints — cost if wrong: later interfaces may require a documented refinement.

Task 1: Ruling: Add DB guards for one active run per physical session and immutable published naming/provenance — prevents two recorders or accidental history renaming independent of service code — cost if wrong: lifecycle code must explicitly stop its old run before starting another.

Task 2: Ruling: Add additive migration 0006_source_draft_requests and an optional variadic request key — synchronous draft retries need persisted outcomes without inventing Worker jobs; six-argument callers stay valid — cost if wrong: one additional migration and cleanup policy for these outcomes.

Task 2: Ruling: M1-B If-Match uses quoted numeric entity tags like M1-A — the draft contract's unquoted regex contradicted the existing API — cost if wrong: clients sending unquoted values receive 422 and must follow the generated contract.

Task 2: Ruling: Keep unconfigured slots in exports with source_state=not_configured, nullable provenance and empty source fields — PRD CH-10 overrides the initial all-configured assumption; an actual decryption failure still aborts the entire file — cost if wrong: import must skip marked empty slots rather than clear an existing source.

Task 2: Ruling: Camera network validation here is the API's configured-CIDR/known-management-address check; fresh internal address checks at Worker connection time remain Task 3/6 work — startup DNS alone cannot prove a connection remains safe — cost if wrong: the later connection gate must be completed before enabling media operations. Skip empty-CIDR and disabled optional-module DNS probes.

Task 3: Ruling: Publish to the isolated development branch through the GitHub connector because native git push has no credentials; compare remote tree SHA with native committed tree before updating its ref, preserving native commits locally — authorized CI execution, no main merge — cost if wrong: commit SHAs differ and final branch synchronization must preserve uncommitted work.

Task 3: Ruling: Debian signed snapshot 20260919T000000Z is available and fixes ffmpeg 7:5.1.9-0+deb12u1; lock all obtainable dependency alternatives so apt's base-dependent choice still needs exact version/hash verification — base image packages remain fixed by digest — cost if wrong: an unlisted download makes CI build fail instead of silently installing.

Task 3/4: Ruling: While fixed-image CI is pending, begin Task 4's independent inbox/spool durability tests against the now-tested typed media interface; Task 3 remains incomplete and no media activation is enabled — real-media results do not affect durable database/fsync semantics — cost if wrong: adapt Completion/probe evidence to the actual Hook contract before either task can pass acceptance.

Task 3: Ruling: Mount a static media-launcher from the existing app build into the unchanged digest-pinned ZLM container. Before executing MediaServer it installs an OUTPUT allowlist in that container's network namespace, then drops NET_ADMIN and prevents privilege gains — the fixed binary lacks a redirect-off option; the host firewall and permanent container count stay unchanged — cost if wrong: Docker hosts must support nftables/NET_ADMIN, and wrapper startup must fail explicitly if the boundary cannot be installed. Known internal/management addresses and special networks are denied; broad camera CIDRs overlapping the Docker connected subnet fail configuration, exact explicitly allowed peer IPs support isolated CI fixtures. This is a Task 3 connection-boundary refinement; verify with the same real redirect sentinel before enabling sources.

Task 4: Ruling: Add migration 0007 protecting stream-session physical identity — the old-run late-tail test showed that run IDs were immutable but their joined physical keys could still change — cost if wrong: workflows must close/create a new session instead of rewriting its key, as the approved generation model already requires. Test uses the pool's registered canonical path, matching real ZLM callbacks.

Task 4: Ruling: Preserve corrupt interrupted spool files, report them, and continue draining valid acknowledged files — an orphan must not indefinitely strand independent callbacks — cost if wrong: operator repair is required for a corrupt file; no unacknowledged data is silently deleted. RED→GREEN spool interruption test passes.

Task 4: Ruling: Execute MediaServer as UID/GID 10001 after installing and dropping the firewall capability; ZLM gets the same private media owner as API/Worker, and unprivileged port binding is enabled only in its network namespace — CI exposed root-owned completed directories that Worker could not rename/remove — cost if wrong: mounted pools must already permit UID 10001, which independent pool checks will report instead of changing arbitrary host ownership. File logs stay in an ephemeral /tmp directory.

Task 4: Ruling: Source-allocated temporary pool probes are wired here via the private source loader; formal-run completion refresh consumes PublishZLMEvidence in Task 5 and controlled idle retesting consumes CheckPool in Task 7, since those runs/scheduler do not exist before then — preserve the approved evidence semantics and avoid fabricated observations — cost if wrong: these consumers remain explicit acceptance obligations before M1-B can finish.

Task 4/5: Ruling: Begin Task 5 deterministic naming/publication tests while Task 4 actual-image pool gate runs; Hook DB/spool contracts passed native race checks and no source activation is enabled — filesystem/DB publication semantics can progress independently of probe acceptance — cost if wrong: adapt evidence to real pool output before either phase is accepted. Task 5 base 92226dd.

Task 5: Ruling: Set only ZLM internal working-file timestamps to UTC and record that rule in the immutable private run receipt, while canonical recordings continue using the selected site IANA timezone — missing-hook recovery must interpret the pinned native timestamp reliably — cost if wrong: the actual CI must verify native basename time equals Hook epoch in UTC before enabling formal recording. Embed no camera credentials in receipts.

Task 5: Ruling: Recovery scan budgets resume via per-Service keyset/run and lexical file cursors rather than repeatedly inspecting only the latest 64 runs/first 2048 entries — regression tests showed permanent starvation of old runs and later files — cost if wrong: cursors are optimization state and restart rescans from the beginning, with durable inbox/segments preventing duplicate media.

Task 5/6: Ruling: Begin Task 6 source-test request/slot tests while the exact committed Task 5 tree runs actual --publish and repeat-deploy CI — these tests do not activate formal sources and the typed Hook/publication contracts passed native tests — cost if wrong: adapt the source lifecycle consumer before Task 5 or 6 acceptance; neither CI gate is waived.

Task 6: Ruling: Add additive0008 source-test slot rows (exact1/2), reserved in the same transaction as the job claim and fenced by that job's live token — limiting handlers after Claim would consume waiting rows' finite retry attempts and cross-Worker counting raced — cost if wrong: one extra migration; expired/completed jobs release capacity by verified live-job state, not an independent timer.

Task 6: Ruling: SourceChange includes an optional test_id on source.test acceptance — queued and failed jobs need an immediately addressable domain result because generic failed job responses have no result payload — cost if wrong: additive API field, regenerated frontend types; existing consumers remain valid.

Task 6: Ruling: ExecuteSourceTest/ExecuteSourceChange reside on recording.Service, with app only consuming registered queue handlers and channel.Execution owning the pinned connection — media and immutable run receipts belong to the existing recording consumer, avoiding a channel→recording package cycle — cost if wrong: adapt internal signatures; public API and phase/fence requirements stay unchanged.

Task 6: Ruling: Retain test-session intent and collect only terminal-job test streams after finite source.test exhaustion — failed ordinary tests must stay bounded without orphaning their temporary proxies — cost if wrong: one narrow cleanup loop, exact channel lock and mapped test UUIDs only; formal source sessions are untouched.

Task 6: Ruling: Same-channel queued source tests reserve neither a global slot nor a retry attempt while a prior source job/switch owns the channel — per-handler rejection would exhaust finite test retries during a 30s/30s source change — cost if wrong: Claim adds a short advisory transaction lock and live-intent filter; unrelated channels still use both global slots.

Task 6/7: Ruling: Begin Task7's independent capacity/status tests while the exact committed Task6 tree runs real --switch CI — native source lifecycle contracts passed (broad race136.185s) and media interface remains unchanged — cost if wrong: adapt the consumer to the real ZLM result; Task6 remains incomplete until its actual switch gate passes, and Task7 cannot claim acceptance meanwhile.

Task 7: Ruling: Add0009 one replaceable runtime lease per channel, sharing the source job pinned advisory lock and fencing against channel configuration version — ten-second health sampling must not create thousands of durable user jobs each day — cost if wrong: one additional runtime lease path to test; DB/connection/fence loss cancels new work and never forces existing upstream recordings to stop.

Task 7: Ruling: Keep source readability proof at5min but require fresh30s paired bitrate proof again before stopping the old continuous recorder — extending rate validity would contradict the explicit capacity contract; an expired proof produces bitrate_unknown and a repeat source test rather than interrupting old media — cost if wrong: users may need to repeat a source test before applying a delayed continuous change.

Task 7: Ruling: Add pool_probe_intents to the unshipped0009 migration and a pinned per-pool advisory owner — a crash after AddProxy but before a run existed otherwise leaves no pool attribution, and concurrent idle/operator/source checks could mistake a live probe for an orphan — cost if wrong: extra small intent rows/one connection during bounded probes; cleanup is restricted to mapped temporary probe sessions and never formal sessions. No new migration version is needed because0009 has not yet been committed or deployed.

Task 7/8: Ruling: Begin Task8's independent import parsing and preview tests while the committed Task7 candidate runs actual recovery CI — parsing never activates sources or changes recording, and the tested source/policy contracts are fixed in the candidate — cost if wrong: adapt import consumers before acceptance; Task7 remains incomplete until exact-tree backend and --recovery pass.

Task 7: Ruling: Give the gateway phase wrapper240s while retaining each inner TLS60s limit — the former120 polls ended a still-allowed lifecycle at~35s and hid its failure; fixture-only component/job diagnostics are emitted on wrapper timeout — cost if wrong: a hung acceptance wrapper waits longer, still bounded, and real runtime failure is not waived.

Task 8: Ruling: Retry accepts explicit ImportSelection items, including latest expected_version, instead of the initial rows-only OpenAPI draft — acknowledging only row numbers could silently overwrite changes made after preview; successful rows remain unselectable, failed rows get a fresh test before apply — cost if wrong: retry API/early generated types and the forthcoming UI must use the revised request; conflicts may require refreshing the row version/confirmation.

Task 8: Ruling: Batch orchestration runs as source.import on recording.Service, with app registering its handler and channel.SourceService.AdvanceImport doing fenced database transitions — existing private source TX helpers atomically create drafts/tests/switch receipts; waiting without a channel lock lets existing child media jobs retain their global two-slot/fence rules — cost if wrong: extra durable parent jobs must stay resumable and cannot be treated as ordinary exhausted tasks.

Task 8: Ruling: Put batch endpoints under /api/v1/source-imports — the initial /channels/source-imports/{batch_id} overlaps Go ServeMux's /channels/{id}/source-revisions and panics on startup — cost if wrong: generated provisional clients must regenerate before use; all six endpoints move together, authorization is unchanged. Multipart/CSRF/safe-error RED404/panic→GREEN3.519s.

Task 7: Ruling: Set application Worker MaxConns24 and API8, lazily opened — four monitor owners plus two tests/four source queues/nested pool probes otherwise exhaust CPU-derived4 and starve their own queries/TLS/renewals; native tests' explicit8 had hidden the production difference — cost if wrong: a busy Worker can use more PostgreSQL connections/memory; bounded owner counts and actual low-core CI must verify the reserve.

Task 7: Ruling: Recovery acceptance waits up to30s across actual10s reconciliation ticks after observed physical absence — a transient reconnect attempt may safely close its failed intent and retry on the next monitor tick; one-call success was stronger than the approved periodic recovery contract — cost if wrong: transient failures within this readiness window are accepted, but no old session/absent recorder may pass and deadline failure remains visible.

Task 8/9: Ruling: Begin independent frontend input/credential lifecycle tests against the committed import/source OpenAPI while exact Task8 Docker acceptance runs — native91.775s verifies these DTOs and request semantics; no real source/recording frontend acceptance is claimed until fixed-image gates and real browser scenarios pass — cost if wrong: adjust the UI consumer if actual CI finds a contract issue; Tasks7/8 remain incomplete meanwhile. Task9 base0a78637, brief read.

Task 9: Ruling: Expose safe parsed password_action in ImportItem metadata — UI cannot infer CSV empty versus JSON missing after upload without retaining plaintext files, and silently defaulting keep would ignore imported credentials — cost if wrong: additive optional enum; old previews fall back to explicit user choice and secrets remain encrypted.

Task 10: Ruling: Task9 exact production UI/media CI is pending; prepare independent joint-acceptance matrix/fixtures now, but do not mark Task9/Task10 complete or create final delivery before its gate. This reuses producer interfaces without changing their semantics; cost if wrong is fixture revision, no deployment or false acceptance.

Task 7/10: Ruling: actual recovery fixture promised two bounded reconnect attempts but 30s total cannot fit two15s attempts plus10s interval/cleanup. New TestRecoveryBudgetAllowsTwoBoundedAttempts RED at30s cutting off second attempt; use75s fixture budget and retain latest6 safe reasons on failure. Production reconnect deadlines/unknown/absence handling unchanged; CI actual recovery still required, and timing budget mismatch is not proof that all media outages are fixed. Cost if wrong: longer failing fixture, not unbounded production retry.

Task 10: Ruling: public media paths may return the exact static SPA fallback (HTTP200) with no media/API response. Native boundary test RED on harmless HTML; verify 401/403/404 or exact root HTML instead, preserving no-public-media requirement without inventing production routing changes. Cost if wrong: boundary classification could miss a nonidentical exposed response; exact body/content-type comparison minimizes this.

Final: Ruling: Start the one fresh whole-branch review on frozen implemented candidate15c61e7 while Task9/10 actual Docker gates run — Task10 itself includes final review, and structural recovery/security review can proceed independently of runtime acceptance; do not mark tasks complete or create final delivery until both gates pass. Cost if wrong: CI fixes must be regraded and verified in the same single fix pass, with no second whole-branch reviewer.

Final: Ruling: Upgrade ready-index/pool_unavailable availability label from Minor to Important/P2 — a verified file on an unavailable mounted pool is not presently usable, and the operator must see that failure rather than a misleading available label. Cost if wrong: a conservative unavailable label until the pool check recovers; file provenance/readiness remains unchanged. Add a regression before correcting the label.

Final: Ruling: Declined WebRTC tickets/revocation/layouts/SSE/Range playback remain M1-C — no M1-B implementation/acceptance claim for them. Cost if wrong: later client work is required.

Final: Ruling: Declined Frigate event/OpenList archive business remains later; core independence from enabled but absent optional services is in scope now. Cost if wrong: modules cannot yet deliver business functions despite status visibility.

Final: Ruling: Declined planned/event/manual recording, deletion/cloud retention/completeness score remain outside approved none/continuous M1-B. Cost if wrong: later features still needed; no claim of full NVR parity.

Final: Ruling: Declined real16–32 codec/throughput and hardware certification requires actual deployment evidence unavailable from two synthetic pairs. Deterministic many-configured scheduler freshness is in scope. Cost if wrong: fleet throughput remains unqualified.

Final: Ruling: Declined RAID/NAS/provisioning is excluded by user directory-pool decision; mounted identity/capacity/publication is tested. Cost if wrong: underlying storage must be managed externally.

Final: Ruling: Declined M1-A two known UI minors remain explicitly deferred. Cost if wrong: feedback visibility/search convenience remains limited.

Final: Ruling: Declined already-admitted import survives preview expiry because it is a durable job; new submission/retry remains TTL gated. Cost if wrong: admitted work can finish after the preview is no longer submit-able.

Final: Ruling: Declined timezone behavior freezes at first durable publication intent as the binding spec requires, not run creation. Cost if wrong: a settings change before publication can affect that run's first final path; publication provenance is still immutable thereafter.

Final: Ruling: Declined corrupt spool/unknown media stays preserved for repair, without guessing identity or deleting. Cost if wrong: operator storage repair is required.

Final: Ruling: Declined identical bounded SPA fallback at private media routes is not a media response; exact body/type comparison remains required. Cost if wrong: classification requires tightening if routing changes.

Final: Ruling: Declined absent local Docker is handled by exact production-image GitHub CI, not a passed local-runtime claim. Cost if wrong: CI-only faults may take an additional cycle to diagnose.

Final: Ruling: Individual runtime bitrate samples record positive frame progress even if adjacent polling jitter is slightly below10s; safetyLine still requires two valid observations at least10s apart within30s, and source-test paired evidence retains its explicit10s wait. RED controlled9.9/10.1s polling cadence reproduced missing fresh pair. Cost if wrong: individual sample cadence is more permissive, but startup capacity cannot bypass the unchanged paired-time gate. This addresses CI37329858939 rather than weakening the capacity contract.

Final: Ruling: A policy/pool-change job with stale target bitrate collects bounded decoded-first-frame and paired real rate samples itself before stopping old recording, then repeats the same filesystem capacity check; unrelated unknown peers still block. Cost if wrong: changing recording settings may take an additional bounded20s, but no unknown/stalled source is admitted.

Final: Ruling: Pool checks try configured sources before the latest fresh successful drafts, retaining alternate-channel/draft fallback and bounded source attempts. Controlled8s recovery budget RED20.727s shows an unavailable historical draft consumed all time despite a healthy current source. Cost if wrong: an unavailable current source may delay a healthy draft up to10s; actual write/file verification failures still do not fall through. The exact fixed-image rollback gate must still prove this candidate.

Final: Ruling: Development/CI Go test image installs production locked media libraries, and mandatory-media flag turns missing FFmpeg/FFprobe into failure rather than a skip. Cost if wrong: initial dev/CI Go builds download locked media tools and take longer; optional native runs without tools still skip honestly, but cannot satisfy acceptance. Required-media absent-tools regression RED1.565s→GREEN5.772s with actual publication/structure cases.

Final: Ruling: Backend workflow budget is30min because its complete media/PG suite may use20min, with separate locked image build/unit/vet overhead. Cost if wrong: a failing workflow may run longer; Go suite20m and all individual media/connection deadlines are unchanged.

Final: Ruling: Real-media browser acceptance reloads the UI between completed source switches/clear and the next edit; its external API job polling can observe completion before UI queries refresh, causing an honest optimistic409. Cost if wrong: this scenario verifies deliberate sequential operator actions rather than zero-delay submissions; production version conflict protection remains unchanged, and failed rollback feedback is asserted before reload. CI37403045837 RED at next draft409, after actual policy activation and first switch succeeded. Safe error-code diagnostic added; no hidden retry or unconditional latest-version overwrite.

Final: Ruling: Before each continuous source apply, real browser acceptance requests an actual fresh pool check through the storage UI in a second authenticated tab; the selected tested revision stays in the original tab. Binding spec pool proof expires30s while formal MP4 completes60s; existing healthy recording cannot manufacture current write proof. Cost if wrong: acceptance/operator source changes require an explicit short pool check and extra UI action; no TTL increase, inferred proof or API admission weakening. CI37403045837 joint RED503 on second apply after source proof succeeded; missing current pool proof is now an explicit fixture precondition.

Final: Ruling: Isolated waitRecoveredSession retries typed zlm.ErrMediaOperation on the existing10s cadence within the unchanged75s budget, matching production Monitor's periodic management-uncertainty behavior. It never evaluates a session after unsuccessful reconciliation, and other errors remain terminal. Cost if wrong: a transient management failure may consume another bounded attempt; persistent uncertainty still fails with no recovery receipt. CI37404369875 actual recovery RED at one management error despite earlier successful switch; TestRecoveryWaitsThroughTransientReconcileFailure RED0.518s→whole media fixture GREEN55.961s includes persistent-unknown rejection. This adjusts the acceptance observer, not source absence classification or production deadlines.

Final: Ruling: Joint bootstrap reads channel version through the existing authorized source/status endpoint instead of a nonexistent single-channel GET. Cost if wrong: the acceptance client depends on the published status DTO version; no new API surface, authorization bypass or production behavior change. CI37404369875 joint RED404 at bootstrap; actual isolated API confirms old GET404 and source/status200 with positive version.

Final: Ruling: Supersede the fixture-only CI37406888801 early after discovering a third identical nonexistent channel GET in its final rollback phase, rather than waiting for a certain404. Prior mandatory-media Linux PG819.601s proves unchanged production code; final exact new candidate still requires all12 gates. Cost if wrong: the interrupted candidate has no complete acceptance receipt and its media recovery observation must be repeated.

Final: Ruling: Final Task10 task-done validates the exact published all12-job CI receipt instead of attempting local test-media.sh --acceptance without a Docker daemon, following the user-selected GitHub CI environment. It checks actual workflow completion, required job names and native/remote tree identity; missing/incomplete/moved candidates fail. Cost if wrong: final acceptance depends on GitHub API availability and trustworthy completed job receipts; it cannot claim an unexecuted local Docker run.

Final: Ruling: Actual-media off acceptance first waits for both decoded main/sub streams after rollback, then requires both within the existing30s readiness window after none; policy jobs stop only recorders while optional sub recovery is periodic. Refresh the UI after pool bind and none before another versioned mutation, extending the already chosen sequential-operator rule. Cost if wrong: brief sub readiness recovery up to30s is accepted, but persistent loss still fails; precondition proves both were available before testing off, and production optimistic versions/deadlines remain unchanged.

Final: Ruling: Independent browser-runtime/media-browser-runtime/joint-media-runtime jobs depend on frontend/M0 but run alongside full backend PG; each builds its own exact-source isolated images and consumes no backend artifact. All12 exact gates remain required. Cost if wrong: a backend failure may waste concurrent runtime compute, but cannot produce accepted CI or deploy production. This removes serial waiting from fixture-only iterations.

Final: Ruling: Joint file and SIGKILL finalizing-identity acceptance use storage_pools.canonical_path, not the API DTO path alias, and share their exact SQL with a real-migration PostgreSQL regression. Cost if wrong: acceptance queries must evolve with schema changes, but no production migration or file verification is bypassed. CI37410251912 joint passed bootstrap/two full files and first API restart/new ready observations, then RED on SQL; both queries independently RED SQLSTATE42703 in native migrated schema.

Final: Ruling: Joint bootstrap obtains a fresh actual source test and real pool check separately for each requested continuous channel, rather than reusing one pre-loop proof across sequential policy jobs. Existing30s rate/pool gates and20s bounded policy refresh are unchanged; diagnostics include only channel number, domain phase, attempt count and proof-valid booleans. Cost if wrong: setup performs extra bounded real tests/checks and takes longer, but no fresh evidence is fabricated or queued failure suppressed. CI37411879424 bootstrap RED130.47s with policy_apply still testing/running and job queued handler_failed before any stop/start; precise failure cause remains unconfirmed until diagnostic/fresh-precondition CI.

Final: Ruling: Before any recording stop/start, a policy job whose formal main is missing or whose pool capacity/write proof has expired durably restores the old policy and fails with source_unavailable/pool_unavailable, releasing the channel guard. Database/lease errors and operations after side effects still retain durable retry/reconciliation. Cost if wrong: an enable request may require an operator retry after the source or pool recovers; it cannot silently remain queued and prevent Monitor recovery. Native real-PG regressions independently RED24.835s with both jobs queued handler_failed/testing and unchanged none; first fix released the missing-main guard but RED69.118s exposed an earlier capacityFor expiry branch, which now has the same preflight refusal.

Final: Ruling: Keep the DB-outage spool gate at80s after CI37413592800 failed it, and add only safe completion/recovery counts, spool/file counts, upstream recorder booleans and fixed-class Hook rejection warnings to distinguish delivery from durability failure. Cost if wrong: CI needs another real outage run and small fixed-class warnings add log noise; no callback payload, URL, media key or credential is logged, and diagnostics cannot satisfy the missing acceptance receipt.

Final: Ruling: During bounded policy/pool target verification, sample actual main-stream frame/rate progress on every retry while the job owns the channel, including the mandatory capacity recovery wait. Monitor is excluded by this same guard; an initially valid pair must not expire without another observer. SafetyLine still requires two valid samples at least10s apart within30s, and capacity recovery still needs double free space plus two healthy observations10s apart. Cost if wrong: bounded recording setting changes make extra private Inspect calls and rate observations; no synthetic rate, extra deadline or stale-proof acceptance is introduced. Native initially valid28s/18s pair plus pool-recovery gate independently RED42.754s as failed recording_change_failed/none, matching the latest CI symptom but not proving its unlogged exact cause. Safe capacity-block reason/count diagnostics accompany the actual candidate.

## Deferred minors

Final: minor (deferred): M1-B-decisions run-timezone wording conflicts with the binding spec's first-publication-intent freeze; runtime follows the binding spec. Cost if deferred: readers of the summary may misunderstand the freeze boundary; no runtime/path change is authorized by this documentation discrepancy. Existing M1-A users/pools feedback remount and timezone search/preview remain deferred.

Final: minor (deferred): Existing M1-A users/pools feedback remount. Cost if deferred: operation success/error may disappear when a version change remounts the card.

Final: minor (deferred): Existing M1-A timezone search/preview. Cost if deferred: selecting a timezone is less convenient; actual IANA timezone choice remains available.
