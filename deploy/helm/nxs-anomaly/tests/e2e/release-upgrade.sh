#!/usr/bin/env bash
# Invoked by kind-networkpolicy.sh in its disposable Calico cluster.
set -euo pipefail
chart=$1
cluster=$2
ns=$3
release=nxs-anomaly
version=1.11.7
work=$(mktemp -d)
mirror="voice-mirror-${cluster}"
cleanup() {
  docker rm -f "$mirror" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
previous=${PREVIOUS_CHART_OCI:-oci://ghcr.io/nixys/nxs-anomaly}
helm pull "$previous" --version "$version" --destination "$work"
old=("$work"/*.tgz)
# Install the released images selected by the published chart, using our
# ephemeral database and API credentials. Only the upgrade uses e2e images.
python3 - "$chart/tests/e2e/values-ci.yaml" > "$work/old.yaml" <<'PY'
import sys, yaml
values = yaml.safe_load(open(sys.argv[1]))
del values["image"], values["frontend"]["image"]
print(yaml.safe_dump(values))
PY
helm template "$release" "${old[0]}" -f "$work/old.yaml" > "$work/rendered.yaml"
images=$(python3 - "$work/rendered.yaml" <<'PY'
import sys, yaml
images = {doc["spec"]["template"]["spec"]["containers"][0]["image"]
          for doc in yaml.safe_load_all(open(sys.argv[1])) if doc and doc["kind"] == "Deployment"}
assert len(images) == 2 and all(img.endswith(":v1.11.7") for img in images), images
print("\n".join(sorted(images)))
PY
)
for ref in $images; do
  docker pull "$ref"
  kind load docker-image "$ref" --name "$cluster"
done
k=(kubectl -n "$ns")
h=(helm -n "$ns")
"${h[@]}" install "$release" "${old[0]}" -f "$work/old.yaml" --wait --timeout 8m

api() {
  local method=$1 path=$2
  shift 2
  kubectl -n monitoring exec prom-probe -- curl --fail-with-body -sS --max-time 20 \
    -X "$method" -H 'X-API-Key: e2e-key' -H 'Content-Type: application/json' \
    "http://$release-api.$ns:8080$path" "$@"
}
user() {
  api POST /api/v1/users -d "{\"name\":\"user$1\",\"phone\":\"+$1\",\"notification_targets\":[\"$2\"]}" | jq -er .id
}
api_user=$(user 101 call)
worker_user=$(user 102 log)
chain=$(api POST /api/v1/escalation-chains \
  -d "{\"name\":\"upgrade\",\"steps\":[{\"kind\":\"NOTIFY_USER\",\"user_ids\":[\"$worker_user\"]}]}" | jq -er .id)
key=$(api POST /api/v1/integrations -d "{\"name\":\"upgrade\",\"default_chain_id\":\"$chain\"}" | jq -er .key)
fire() {
  api POST "/integrations/v1/webhook/$key" -d "{\"title\":\"$1\",\"status\":\"firing\",\"dedupe_key\":\"$1\",\"labels\":{\"alertname\":\"$1\"}}" | jq -er .alert.alert_group_id
}
old_group=$(fire before-upgrade)

# Exercise an internal pull-through mirror. The node requests the exact index
# digest and platform layers through the mirror, as it would on a private site.
image=$(helm show values "$chart" | python3 -c 'import sys,yaml; print(yaml.safe_load(sys.stdin)["asterisk"]["image"])')
digest=${image##*@}
[[ $digest =~ ^sha256:[a-f0-9]{64}$ ]]
docker run -d --name "$mirror" --network kind \
  -e REGISTRY_PROXY_REMOTEURL=https://registry-1.docker.io registry:2 >/dev/null
mirror_ip=$(docker inspect -f '{{(index .NetworkSettings.Networks "kind").IPAddress}}' "$mirror")
ref="$mirror_ip:5000/${image%%:*}@$digest"
for attempt in 1 2 3; do
  if docker exec "$cluster-control-plane" ctr -n k8s.io images pull --plain-http "$ref"; then break; fi
  [[ $attempt != 3 ]] || exit 1
  sleep 2
done

"${k[@]}" create configmap sip-peer --from-file=peer.py="$chart/tests/e2e/asterisk-call.py"
"${k[@]}" apply -f - <<YAML
apiVersion: v1
kind: Pod
metadata: {name: sip-peer}
spec:
  restartPolicy: Never
  containers:
    - name: registrar
      image: "$ref"
      imagePullPolicy: IfNotPresent
      command: [python3, /fixture/peer.py, --registrar]
      volumeMounts: [{name: fixture, mountPath: /fixture}]
  volumes: [{name: fixture, configMap: {name: sip-peer}}]
YAML
"${k[@]}" wait --for=condition=Ready pod/sip-peer --timeout=90s
peer=$("${k[@]}" get pod sip-peer -o jsonpath='{.status.podIP}')
"${k[@]}" create secret generic voice --from-literal=ami-password=testami --from-literal=sip-password=testsip
"${h[@]}" upgrade "$release" "$chart" --reset-then-reuse-values \
  --set image.repository=nxs-anomaly,image.tag=e2e,frontend.image.repository=nxs-anomaly-frontend,frontend.image.tag=e2e \
  --set asterisk.enabled=true,asterisk.existingSecret=voice,asterisk.username=test,asterisk.trunkPort=5090 \
  --set-string asterisk.callerID=103,asterisk.trunkHost="$peer",asterisk.image="$ref" \
  --set networkPolicy.enabled=true --set-string "asterisk.providerCIDRs[0]=$peer/32" \
  --set 'networkPolicy.extraIngress[0].namespaceSelector.matchLabels.kubernetes\.io/metadata\.name=monitoring' \
  --wait --timeout 8m
for component in api worker asterisk; do
  "${k[@]}" rollout status "deploy/$release-$component" --timeout=120s
done
api GET "/api/v1/alert-groups/$old_group" | jq -e --arg id "$old_group" '.id == $id and .status == "open"'
wait_for_peer() {
  for _ in $(seq 1 60); do
    if "${k[@]}" logs sip-peer | grep -Fqx "$1"; then return 0; fi
    sleep 2
  done
  "${k[@]}" logs sip-peer >&2
  return 1
}
wait_for_peer 'REGISTER accepted'
api POST "/api/v1/users/$api_user/test-notification" -d '{"channel":"call"}' | jq -e '.status == "delivered"'
wait_for_peer 'Received speech for +101'
api PATCH "/api/v1/users/$worker_user" -d '{"notification_targets":["call"]}' | jq -e --arg id "$worker_user" '.id == $id'
new_group=$(fire after-upgrade)
test "$new_group" != "$old_group"
wait_for_peer 'Received speech for +102'
echo "Published $version upgraded; REGISTER, API call and worker escalation passed under Calico."
