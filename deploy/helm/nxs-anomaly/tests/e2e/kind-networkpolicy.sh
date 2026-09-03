#!/usr/bin/env bash
# NetworkPolicy enforcement smoke: proves the extraIngress allowance actually
# works (and that everything else is actually blocked) against a CNI that
# enforces NetworkPolicy. kind's default CNI (kindnet) does NOT enforce
# NetworkPolicy at all, so a policy with a typo or a wrong selector would still
# pass a plain kind-smoke run silently — this needs Calico.
#
# Cluster layout:
#   - chart installed in ${NS} with networkPolicy.enabled=true and extraIngress
#     scoped to a namespaceSelector matching the "monitoring" namespace.
#   - a curl pod in "monitoring"  -> must reach API :8080/health and worker
#     :8081/live (the allowed source).
#   - a curl pod in "attacker", an unrelated namespace with no special labels
#     -> must NOT reach either port (proves default-deny + the worker-ingress
#     policy are actually enforced, not just present in the rendered YAML).
#
# Requires: docker, kind, kubectl, helm. Run from anywhere in the repo.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && pwd)"
CHART_DIR="${REPO_ROOT}/deploy/helm/nxs-anomaly"
VALUES="${CHART_DIR}/tests/e2e/values-ci.yaml"
CALICO_MANIFEST="${CALICO_MANIFEST:-https://raw.githubusercontent.com/projectcalico/calico/v3.28.2/manifests/calico.yaml}"
CLUSTER="${CLUSTER:-nxs-anomaly-netpol-$$}"
# Pinned to a cgroup v1-capable node image — see the same block in kind-smoke.sh.
KIND_NODE_IMAGE="${KIND_NODE_IMAGE:-kindest/node:v1.32.11@sha256:5fc52d52a7b9574015299724bd68f183702956aa4a2116ae75a63cb574b35af8}"
NS="nxs-anomaly-e2e"
RELEASE="nxs-anomaly"
KEEP="${KEEP_CLUSTER:-0}"
PROBE_TIMEOUT=6

# Job-local kubeconfig only kind writes — see the same block in kind-smoke.sh for
# why an unset KUBECONFIG is dangerous under the Kubernetes executor.
export KUBECONFIG="${KUBECONFIG:-$(mktemp -d)/kubeconfig}"

log() { echo -e "\n=== $* ==="; }

# Kubelet journal from the retained kind node — see the same block in kind-smoke.sh.
dump_node_diagnostics() {
  node="${CLUSTER}-control-plane"
  docker inspect "${node}" >/dev/null 2>&1 || return 0
  echo "----- kubelet on ${node} -----" >&2
  docker exec "${node}" journalctl -u kubelet --no-pager -n 120 2>&1 | sed 's/^/  /' >&2 || true
  echo "----- node environment -----" >&2
  docker info --format '  docker: cgroups v{{.CgroupVersion}}/{{.CgroupDriver}} storage={{.Driver}} kernel={{.KernelVersion}}' >&2 2>/dev/null || true
  docker exec "${node}" sh -c 'sysctl fs.inotify.max_user_instances fs.inotify.max_user_watches; nproc; free -m' 2>&1 | sed 's/^/  /' >&2 || true
}

dump_diagnostics() {
  dump_node_diagnostics
  if [ ! -s "${KUBECONFIG}" ]; then
    echo "----- no kind cluster was created; nothing to diagnose -----" >&2
    return
  fi
  echo "----- diagnostics -----" >&2
  kubectl get networkpolicy -n "${NS}" -o yaml 2>&1 | sed 's/^/  /' >&2 || true
  kubectl -n "${NS}" get pods -o wide 2>&1 | sed 's/^/  /' >&2 || true
  kubectl -n monitoring get pods -o wide 2>&1 | sed 's/^/  /' >&2 || true
  kubectl -n attacker get pods -o wide 2>&1 | sed 's/^/  /' >&2 || true
  # Container logs for anything not ready. A pod that cannot reach its database
  # looks identical from `get pods` to one that crashed on a bad config; only the
  # log says which. kind-smoke.sh has had this since it was written.
  for p in $(kubectl -n "${NS}" get pods -o name 2>/dev/null); do
    ready="$(kubectl -n "${NS}" get "$p" -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null || echo false)"
    if [ "${ready}" != "true" ]; then
      echo "  --- logs $p ---" >&2
      kubectl -n "${NS}" logs "$p" --all-containers --tail=40 2>&1 | sed 's/^/    /' >&2 || true
      kubectl -n "${NS}" logs "$p" --all-containers --previous --tail=40 2>&1 | sed 's/^/    prev: /' >&2 || true
    fi
  done
}

cleanup() {
  rc=$?
  if [ "$rc" != "0" ]; then dump_diagnostics; fi
  if [ "${KEEP}" != "1" ]; then
    kind delete cluster --name "${CLUSTER}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

for bin in docker kind kubectl helm; do
  command -v "${bin}" >/dev/null 2>&1 || { echo "missing: ${bin}"; exit 1; }
done

log "Building images"
docker build -t nxs-anomaly:e2e "${REPO_ROOT}"
docker build -t nxs-anomaly-frontend:e2e "${REPO_ROOT}/frontend"

log "Creating kind cluster ${CLUSTER} with the default CNI disabled (Calico enforces NetworkPolicy; kindnet does not)"
cat <<'EOF' | kind create cluster --name "${CLUSTER}" --wait 0s --retain --image "${KIND_NODE_IMAGE}" --config -
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true
  podSubnet: 192.168.0.0/16
EOF

log "Installing Calico"
kubectl apply -f "${CALICO_MANIFEST}"
kubectl -n kube-system rollout status deployment/calico-kube-controllers --timeout=180s
kubectl -n kube-system rollout status daemonset/calico-node --timeout=180s
log "Waiting for nodes to go Ready (blocked on CNI until Calico is up)"
kubectl wait --for=condition=Ready nodes --all --timeout=180s

log "Loading images into kind"
# Analytics images are loaded only where the analytics leg exists; the list is a
# variable so the edition without it drops whole lines rather than editing a
# continued command.
ANALYTICS_IMAGES=""
# shellcheck disable=SC2086  # deliberate word splitting: the list may be empty
for img in postgres:17-alpine curlimages/curl:8.10.1 ${ANALYTICS_IMAGES}; do
  docker pull "${img}" >/dev/null 2>&1 || true
done
# shellcheck disable=SC2086
kind load docker-image nxs-anomaly:e2e nxs-anomaly-frontend:e2e postgres:17-alpine \
  curlimages/curl:8.10.1 ${ANALYTICS_IMAGES} \
  --name "${CLUSTER}"

log "Creating monitoring/attacker namespaces and probe pods"
kubectl create namespace monitoring
kubectl create namespace attacker
# A namespace is usable before its default ServiceAccount exists: the controller
# creates that asynchronously, and `kubectl run` right after `kubectl create
# namespace` fails with "serviceaccount \"default\" not found" on a cluster
# that is still settling. Seen on a loaded machine, and it reads like a policy
# failure rather than the race it is.
for ns in monitoring attacker; do
  for _ in $(seq 1 30); do
    kubectl -n "${ns}" get serviceaccount default >/dev/null 2>&1 && break
    sleep 2
  done
done
# kubernetes.io/metadata.name is auto-populated by the API server since 1.21,
# which is exactly what values-production.yaml's example extraIngress matches.
kubectl run prom-probe -n monitoring --image=curlimages/curl:8.10.1 --restart=Never --command -- sleep infinity
kubectl run attacker-probe -n attacker --image=curlimages/curl:8.10.1 --restart=Never --command -- sleep infinity
kubectl -n monitoring wait --for=condition=Ready pod/prom-probe --timeout=60s
kubectl -n attacker wait --for=condition=Ready pod/attacker-probe --timeout=60s

log "helm install (networkPolicy.enabled, extraIngress scoped to the monitoring namespace)"
kubectl create namespace "${NS}" >/dev/null 2>&1 || true
ANALYTICS_SET=""
# shellcheck disable=SC2086  # deliberate word splitting: the list may be empty
if ! helm upgrade --install "${RELEASE}" "${CHART_DIR}" -n "${NS}" -f "${VALUES}" \
  --set networkPolicy.enabled=true \
  ${ANALYTICS_SET} \
  --set 'networkPolicy.extraIngress[0].namespaceSelector.matchLabels.kubernetes\.io/metadata\.name=monitoring' \
  --wait --timeout 10m; then
  echo "FAIL: the release did not install under the policy."
  exit 1
fi

FULLNAME="${RELEASE}"
kubectl -n "${NS}" rollout status "deploy/${FULLNAME}-api" --timeout 180s
kubectl -n "${NS}" rollout status "deploy/${FULLNAME}-worker" --timeout 180s
kubectl -n "${NS}" rollout status "deploy/${FULLNAME}-frontend" --timeout 180s

API_HOST="${FULLNAME}-api.${NS}.svc.cluster.local:8080"
WORKER_HOST="${FULLNAME}-worker.${NS}.svc.cluster.local:8081"
FRONTEND_HOST="${FULLNAME}-frontend.${NS}.svc.cluster.local:8080"

probe() {
  local ns="$1" pod="$2" host="$3" path="$4"
  kubectl -n "${ns}" exec "${pod}" -- \
    curl -sS -o /dev/null -w '%{http_code}' --max-time "${PROBE_TIMEOUT}" "http://${host}${path}"
}

log "monitoring namespace must reach the API"
code="$(probe monitoring prom-probe "${API_HOST}" /health || true)"
[ "${code}" = "200" ] || { echo "FAIL: expected 200 from monitoring->API, got '${code}'"; exit 1; }

log "monitoring namespace must reach the worker telemetry port"
code="$(probe monitoring prom-probe "${WORKER_HOST}" /live || true)"
[ "${code}" = "200" ] || { echo "FAIL: expected 200 from monitoring->worker, got '${code}'"; exit 1; }

# Both halves of the frontend policy in one request. /health on the frontend is
# an nginx location that proxies to the API, so a 200 proves the browser side
# could reach nginx AND nginx could reach the API. Until the frontend got its own
# policy it had neither: the default-deny covered it and no rule allowed it, so
# turning networkPolicy on took the UI down — and this suite passed anyway,
# because it only ever probed the API and worker ports.
log "monitoring namespace must reach the frontend, and the frontend must reach the API"
code="$(probe monitoring prom-probe "${FRONTEND_HOST}" /health || true)"
[ "${code}" = "200" ] || {
  echo "FAIL: expected 200 from monitoring->frontend->API, got '${code}'."
  echo "      A non-200 here is the frontend policy: no ingress means nginx was"
  echo "      unreachable, no egress means nginx could not proxy to the API."
  exit 1
}

log "attacker namespace must NOT reach the frontend"
if code="$(probe attacker attacker-probe "${FRONTEND_HOST}" /)"; then
  echo "FAIL: attacker->frontend should have been blocked, got HTTP ${code}"
  exit 1
fi
echo "blocked as expected (curl exited non-zero)"

log "attacker namespace must NOT reach the API (expect curl timeout/failure, not 200)"
if code="$(probe attacker attacker-probe "${API_HOST}" /health)"; then
  echo "FAIL: attacker->API should have been blocked, got HTTP ${code}"
  exit 1
fi
echo "blocked as expected (curl exited non-zero)"

log "attacker namespace must NOT reach the worker telemetry port"
if code="$(probe attacker attacker-probe "${WORKER_HOST}" /live)"; then
  echo "FAIL: attacker->worker should have been blocked, got HTTP ${code}"
  exit 1
fi
echo "blocked as expected (curl exited non-zero)"

log "NETWORKPOLICY ENFORCEMENT SMOKE PASSED"
