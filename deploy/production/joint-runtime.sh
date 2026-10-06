#!/usr/bin/env bash
# Sourced only by the isolated media-e2e wrapper after actual UI acceptance.
[[ ${ONE_NVR_JOINT_ISOLATED:-} == yes && ${#compose[@]} -gt 0 ]] || { printf 'Requires isolated joint fixture.\n' >&2; return 2; }
joint_phase() {
 "${compose[@]}" run --rm --no-deps -e "ONE_NVR_JOINT_PHASE=$1" runner go test -tags gateway_runtime ./tests/integration -run '^TestGatewayMediaRuntimeJoint$' -count=1 -timeout 5m -v
}
# The UI rollback deliberately killed camera publisher zero. Restore only the
# synthetic publisher container before configuring the second actual channel.
"${compose[@]}" up -d --no-deps --force-recreate fixture
joint_phase bootstrap
for service in api worker both; do
 joint_phase snapshot
 case "$service" in
  api) "${compose[@]}" restart api ;;
  worker)
   # Occupy the released ordinary address to deterministically reproduce Docker
   # peer relocation. The private Hook alias must survive without restarting ZLM.
   old_peer=$(docker inspect --format "{{range \$name, \$network := .NetworkSettings.Networks}}{{if eq \$name \"${project}_default\"}}{{\$network.IPAddress}}{{end}}{{end}}" "$("${compose[@]}" ps -q worker)")
   [[ "$old_peer" =~ ^172\.30\.253\.[0-9]+$ ]] || return 1
   "${compose[@]}" stop worker
   "${compose[@]}" rm -f worker
   peer_reservation=$(docker run -d --network "${project}_default" --ip "$old_peer" --entrypoint sleep one-nvr/app:ci 300)
   "${compose[@]}" up -d --no-deps worker
   new_peer=$(docker inspect --format "{{range \$name, \$network := .NetworkSettings.Networks}}{{if eq \$name \"${project}_default\"}}{{\$network.IPAddress}}{{end}}{{end}}" "$("${compose[@]}" ps -q worker)")
   [[ -n $new_peer && $new_peer != "$old_peer" ]] || { printf 'Worker peer relocation not exercised.\n' >&2; return 1; }
   printf 'Worker ordinary address changed; private callback continuity required.\n'
   ;;

  both) "${compose[@]}" restart api worker ;;
 esac
 joint_phase restart
 if [[ -n $peer_reservation ]]; then docker rm -f "$peer_reservation" >/dev/null; peer_reservation=; fi
done
joint_phase snapshot
"${compose[@]}" stop postgres
joint_phase spool
"${compose[@]}" up -d --wait postgres
joint_phase db
joint_phase snapshot
zlm_before=$("${compose[@]}" ps -q zlm)
joint_phase tls
[[ $("${compose[@]}" ps -q zlm) == "$zlm_before" ]] || { printf 'TLS update recreated ZLM.\n' >&2; return 1; }
joint_phase snapshot
"${compose[@]}" restart zlm
joint_phase zlm
joint_phase snapshot
export ONE_NVR_E2E_MODULES=yes
"${compose[@]}" up -d --no-deps --force-recreate api worker
joint_phase modules
# Optional DNS is absent; a new media connection must still recover.
joint_phase snapshot
"${compose[@]}" restart zlm
joint_phase zlm
"${compose[@]}" run --rm --no-deps runner go test -tags gateway_runtime ./tests/integration -run '^TestGatewayMediaRuntimeBoundaries$' -count=1 -v
joint_phase snapshot
# Freezing the real Worker preserves its network identity for private Hook
# delivery, while the independent publisher is killed at the actual Move.
"${compose[@]}" pause worker
for phase in before after; do
 "${compose[@]}" run --rm --no-deps -e "ONE_NVR_PUBLISH_PHASE=$phase" runner go test -tags gateway_runtime ./tests/integration -run '^TestGatewayMediaRuntimePublishCrash$' -count=1 -timeout 5m -v
done
"${compose[@]}" unpause worker
joint_phase restart
joint_phase snapshot
joint_phase rollback-failure
"${compose[@]}" up -d --no-deps --force-recreate fixture
joint_phase rollback-recovery
# Produced only after every real assertion above succeeded. No raw config,
# completion payload, camera URL, PEM, trace or component credential is exported.
"${compose[@]}" run --rm --no-deps --entrypoint sh runner -ec 'printf "%s\n" "{\"api_restart\":true,\"worker_restart\":true,\"both_restart\":true,\"database_outage_spool\":true,\"tls_rotation_two_recorders\":true,\"same_zlm_during_tls\":true,\"zlm_restart_new_runs\":true,\"optional_modules_absent\":true,\"optional_modules_reconnect\":true,\"both_source_rollback_failure\":true,\"publisher_sigkill_before_after_move\":true}" > /results/joint-runtime.json'
