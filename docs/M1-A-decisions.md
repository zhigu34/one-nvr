# M1-A 实施决策与审查记录

以下保留全部实施裁定、独立审查的取舍与暂缓项。运行证据见 [验证报告](M1-A-validation.md)。

## 独立审查

一次独立审查针对 `349cd0a..20a2b55`，发现四项 Important、两项 Minor，无确认的 Critical。四项 Important 已在真实数据库/浏览器单元回归中复现并修复；实际 Docker 最终复核仍在等待。暂缓项完整列于下方，没有第二次审查循环。

## 全部裁定与暂缓项

- Ruling: Use downloaded, checksum-verified temporary native tooling for local tests because this macOS host has Node but no Go or Docker daemon. Production deployment and reproducible test entrypoints remain Docker. Cost if wrong: native tests cannot certify Linux images or deployment; these checks remain pending until an actual daemon is available.

- Ruling: Relocate temporary Homebrew bottle executable paths to its extracted share/lib directories; only test tool files changed, no host installation. Cost: native Darwin PostgreSQL does not certify the Bookworm image.

- Ruling: Signed pre-auth CSRF is returned by setup/status with an HttpOnly cookie, because setup/login have no session yet but still require CSRF. Session mutations use a distinct HMAC token derived from the raw session. Cost: frontend must perform this handshake; OpenAPI updated first.

- Ruling: Registration keeps a successfully published immutable marker if the DB/audit transaction fails, then same-site/same-canonical-path retry reuses its UUID. Empty-pool deregistration also keeps the marker and mounted root. Why: filesystem publication and PostgreSQL commits cannot be atomically coupled; no identity/file deletion is needed. Cost if wrong: explicit operator recovery is required for corrupt/foreign identities; media reference tables in M1-B must prevent deletion.

- Ruling: Pool business used_bytes is null until the recording index exists; available/total capacity uses real statfs evidence, deduplicated by filesystem identity, with conservative shared free space. Low-space blocking floor is 10 GiB in M1-A; adaptive bitrate and recording hysteresis belong to M1-B/M2. Cost: current UI cannot estimate pool retention or trigger cleanup.

- Ruling: Disabling a default pool clears its default flag; choosing another default increments the previous pool version. Why: a disabled target cannot remain a recording default. Cost: a replacement must be explicitly chosen; no silent fallback.

- Ruling: HTTP TLS apply/rollback requests return409 tls_https_disabled; uploads and directory checks still retain validated candidates. Why: no TLS listener exists in explicitly selected HTTP mode. Cost: HTTPS activation requires the authorized deployment scheme change, not a hidden API port switch.

- Ruling: Input content is sampled every10s; validation of an unchanged invalid pair backs off30s, while changed contents are validated immediately. Snapshot/DB commit ambiguity leaves an unreferenced private version rather than deleting potentially committed material; no automatic version cleanup in this phase. Cost: orphan snapshots may require administrative recovery after persistent DB failures; they are never selected without a DB reference.

- Ruling: Gateway compares public DER-chain digest as well as leaf fingerprint, because an unchanged leaf cannot prove a chain-only renewal took effect. Cost: Nginx must serve the imported chain in exactly its verified order; actual-container tests will verify this behavior.

- Ruling: If neither new nor old listener can be verified, keep the original gateway intent and worker lease pending without a terminal receipt. Why: later recovery must not contradict an already committed failure. Cost: administrator sees a long-running application until service recovers; competing applications are blocked.

- Ruling: Task7 adds private snapshot manifests and DER-chain metadata before the first formal production deployment. M0 is separate and untouched; pre-release Task6 developer snapshots are not an accepted upgrade target. Cost: any manually deployed intermediate development snapshot requires re-import before applying it; no destructive auto-repair is performed.

- Ruling: Begin independent Task8 frontend/API work while immutable Task7 commit awaits GitHub runtime acceptance, keeping Task7 uncompleted until actual CI passes. Why: GitHub queued execution should not idle authorized frontend development; code changes do not change the running commit. Cost: a gateway failure may require an additional isolated fix before combined final acceptance.

- Ruling: Add admin-only /channel-slots exposing only permanent slot IDs/names/numbers for grant editing, independent of video grants — an administrator without viewing rights must still assign rights — cost if wrong: slot metadata is visible to site administrators, never video URLs or permissions bypass.

- Ruling: Begin independent Task9 deployment/hardware code while Task8 immutable browser commit awaits CI, without completingTask8 early — avoid idle build queue — cost: isolated frontend failure would require fix first. Task8 now accepted.

- Ruling: No camera/media environment is available in GitHub-hosted CI; prove container identity stability while reserving two-stream recording continuity and hardware driver/business capacity for real deployment — spec explicitly permits untested media evidence — cost: M1-C/M3 need those exit checks before claims.

- Ruling: Distinct __Host-prefixed HTTPS cookie names from HTTP — necessary for allowed protocol switches without clearing browser storage and protects host scoping — cost: pre-release HTTPS sessions require re-login once; protocol change already revokes sessions.

- Final: Ruling: Start the single read-only whole-branch review against immutable20a2b55 while its Docker acceptance37203995849 runs — all implementation is present and native regression checks pass, so CI and inspection are independent; Task9 remains incomplete until actual CI succeeds — cost if wrong: runtime failures must be fixed and regression-tested before acceptance, reviewer verdict alone cannot close the task. No second reviewer or review loop.

- Final: Ruling: tls.apply alone retries reconciliation beyond the ordinary retry count with existing bounded backoff — a durable external change must retain a fenced route to verified domain completion — cost: an unrecoverable dependency keeps application pending and adds one attempt per backoff; ordinary jobs stay bounded, verified permanent failures remain terminal.

- Final: Ruling: read requests validate sessions without renewing idle expiry; visible pointer/key/scroll interactions renew via CSRF-protected POST at most once per minute — background polls must not keep unattended management sessions alive — cost: a custom client must explicitly renew during human activity; an inactive viewer expires after30min even if video continues.

- Final: Minor deferred: users/pools id:version row remount loses success notices/pool-local job display — saved data and centralized jobs persist, no business loss; defer consistent form-state refinement.

- Final: Minor deferred: initialization has Shanghai/UTC only, and settings timezone select lacks search/selection preview — backend supports fullIANA and post-setup selection, no recording exists yet; defer picker refinement.

- Final: Ruling: reviewer set aside camera/recording/playback/media permissions/events/cloud actions — remain explicit M1-B/C/M2/M2.1 scope, no claims of usable NVR delivery — cost: user keeps M0/otherNVR until these phases.

- Final: Ruling: reviewer set aside media continuity/N5105Intel/16–32capacity — evidence is unavailable, reserve real-device exit checks — cost: CI cannot certify actual deployment load.

- Final: Ruling: reviewer set aside nativeOpenList management/WebDAV targets — foundation only observes component, later cloud phase implements targets independently — cost: cloud switch cannot archive yet.

- Final: Ruling: reviewer set aside comprehensive upstream visual/accessibility audit — review focused own business paths and tests, retained generic upstream components — cost: unseen upstream UI issues may remain.

- Final: Ruling: reviewer accepted null poolbusiness-used/fixed10GiB threshold — recording index/adaptive storage belong later, current values honest — cost: no retention estimate yet.

- Final: Ruling: reviewer accepted retained markers/orphan snapshots — recoverable identities take priority over cross-resource auto-deletion — cost: operator recovery and future safe cleanup required.

- Final: Ruling: reviewer accepted certificate-renew keepssessions, protocol-change revokes, HTTPS __Host naming — fulfills session boundaries without pre-release compatibility — cost: old developer HTTPS session needs one re-login.

- Final: Ruling: reviewer accepted invalid directory TLS input refuses self-signed fallback — explicitly selected certificate source remains authoritative — cost: operator must repair input before HTTPS can start.

- Final: Ruling: reviewer accepted private/selfsigned TLS chain validation without client-trust guarantee — show verification metadata but don't claim public trust — cost: client trust remains operator's responsibility.

- Final: Ruling: reviewer declined pendingDocker acceptance/absentnativeDB at its reviewtime — author subsequently ran restored isolatedPG and awaits final realCI, no inference from reviewer verdict — cost: final completion requires actual CI evidence.
