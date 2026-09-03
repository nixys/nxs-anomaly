#!/usr/bin/env bash
# BETA-040 Helm chart install/upgrade smoke on a throwaway kind cluster.
#
# Builds the backend and frontend images, loads them into kind, installs the
# chart with bundled PostgreSQL and an inline dev secret, waits for every
# workload to roll out, runs `helm test` (curls the API /health), then performs
# a `helm upgrade` and re-checks the rollout. Deletes the cluster on exit.
#
# Requires: docker, kind, kubectl, helm. Run from anywhere in the repo.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && pwd)"
CHART_DIR="${REPO_ROOT}/deploy/helm/nxs-anomaly"
VALUES="${CHART_DIR}/tests/e2e/values-ci.yaml"
CLUSTER="${CLUSTER:-nxs-anomaly-helm-$$}"
# Pinned, and pinned to 1.32 on purpose. kind 0.31 defaults to kindest/node:v1.35.0,
# whose kubelet refuses to start on a cgroup v1 host ("kubelet is configured to not
# run on a host using cgroup v1" — v1.35 flipped FailCgroupV1 to true by default).
# The CI runner nodes are cgroup v1 (kernel 5.15, cgroupfs driver), so every kind
# cluster died in kubeadm init there. 1.32 also matches the kubectl pinned in
# .gitlab-ci.yml. The digest is the one published with kind v0.31.0.
KIND_NODE_IMAGE="${KIND_NODE_IMAGE:-kindest/node:v1.32.11@sha256:5fc52d52a7b9574015299724bd68f183702956aa4a2116ae75a63cb574b35af8}"
NS="nxs-anomaly-e2e"
RELEASE="nxs-anomaly"
KEEP="${KEEP_CLUSTER:-0}"

# Pin kubeconfig to a job-local file that only kind writes. With KUBECONFIG unset
# and no ~/.kube/config, kubectl inside a Kubernetes-executor CI job falls back to
# the mounted service-account token and talks to the *runner's own* cluster. That
# is what produced the misleading "pods is forbidden ... in the namespace
# nxs-anomaly-e2e" diagnostics on a run where the kind cluster was never created.
export KUBECONFIG="${KUBECONFIG:-$(mktemp -d)/kubeconfig}"

log() { echo -e "\n=== $* ==="; }

# kubeadm inside the kind node is where a nested-container (dind) environment
# breaks, and its own error only says "the kubelet is not healthy". kind create is
# run with --retain so the node container survives the failure and the kubelet
# journal is still readable here; without it kind deletes the node and the actual
# reason is gone.
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
  echo "----- diagnostics (namespace ${NS}) -----" >&2
  kubectl -n "${NS}" get pods -o wide 2>&1 | sed 's/^/  /' >&2 || true
  kubectl -n "${NS}" get events --sort-by=.lastTimestamp 2>&1 | tail -30 | sed 's/^/  /' >&2 || true
  for p in $(kubectl -n "${NS}" get pods -o name 2>/dev/null); do
    restarts="$(kubectl -n "${NS}" get "$p" -o jsonpath='{.status.containerStatuses[0].restartCount}' 2>/dev/null || echo 0)"
    ready="$(kubectl -n "${NS}" get "$p" -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null || echo false)"
    if [ "${ready}" != "true" ] || [ "${restarts:-0}" != "0" ]; then
      echo "  --- logs $p (restarts=${restarts}) ---" >&2
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

log "Creating kind cluster ${CLUSTER}"
kind create cluster --name "${CLUSTER}" --wait 120s --retain --image "${KIND_NODE_IMAGE}"

log "Loading images into kind"
# Pre-load the bundled Postgres and helm-test images too, so kind nodes never
# have to pull them (faster, and robust on a flaky network).
docker pull postgres:17-alpine >/dev/null 2>&1 || true
docker pull curlimages/curl:8.10.1 >/dev/null 2>&1 || true
kind load docker-image nxs-anomaly:e2e nxs-anomaly-frontend:e2e postgres:17-alpine curlimages/curl:8.10.1 --name "${CLUSTER}"

log "helm lint"
helm lint "${CHART_DIR}" -f "${VALUES}"

log "helm install"
kubectl create namespace "${NS}" >/dev/null 2>&1 || true
helm upgrade --install "${RELEASE}" "${CHART_DIR}" -n "${NS}" -f "${VALUES}" --wait --timeout 5m

# Object names come from the chart fullname, which collapses to the release name
# because the release name already contains the chart name.
FULLNAME="${RELEASE}"
log "Waiting for rollouts"
for c in api worker frontend; do
  kubectl -n "${NS}" rollout status "deploy/${FULLNAME}-${c}" --timeout 180s
done
kubectl -n "${NS}" rollout status "statefulset/${FULLNAME}-postgresql" --timeout 180s

log "helm test (API /health)"
helm test "${RELEASE}" -n "${NS}" --timeout 120s

log "helm upgrade (scale API to 2, flip a config value)"
helm upgrade "${RELEASE}" "${CHART_DIR}" -n "${NS}" -f "${VALUES}" \
  --set api.replicaCount=2 --set config.LOG_LEVEL=debug --wait --timeout 5m
kubectl -n "${NS}" rollout status "deploy/${FULLNAME}-api" --timeout 180s
test "$(kubectl -n "${NS}" get deploy "${FULLNAME}-api" -o jsonpath='{.spec.replicas}')" = "2"

log "helm test after upgrade"
helm test "${RELEASE}" -n "${NS}" --timeout 120s

log "SMOKE PASSED"
