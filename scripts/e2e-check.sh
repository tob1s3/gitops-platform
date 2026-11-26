#!/usr/bin/env bash
# Проверки после terraform apply: Argo CD выкатил staging из Git, сервис работает,
# production ждёт ручного решения.
set -euo pipefail

NS_ARGO=argocd
TIMEOUT=${TIMEOUT:-600}

wait_for() { # описание, команда...
  local what=$1; shift
  local deadline=$((SECONDS + TIMEOUT))
  until "$@" >/dev/null 2>&1; do
    if ((SECONDS > deadline)); then
      echo "timeout: $what"; return 1
    fi
    sleep 5
  done
  echo "ok: $what"
}

app_field() { # приложение, jsonpath
  kubectl -n "$NS_ARGO" get application "$1" -o "jsonpath=$2" 2>/dev/null
}

wait_for "applications created by app of apps" kubectl -n "$NS_ARGO" get application pulse-staging pulse-production
wait_for "pulse-staging synced"  bash -c "[ \"\$(kubectl -n $NS_ARGO get application pulse-staging -o jsonpath='{.status.sync.status}')\" = Synced ]"
wait_for "pulse-staging healthy" bash -c "[ \"\$(kubectl -n $NS_ARGO get application pulse-staging -o jsonpath='{.status.health.status}')\" = Healthy ]"

echo "staging revision: $(app_field pulse-staging '{.status.sync.revision}')"
kubectl -n pulse-staging rollout status deployment/pulse --timeout=120s
kubectl -n pulse-staging get pods -o wide

# Namespace создан Argo CD с политикой Pod Security «restricted».
psa=$(kubectl get namespace pulse-staging -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}')
[ "$psa" = restricted ] || { echo "namespace is not restricted: '$psa'"; exit 1; }
echo "ok: pod security = restricted"

# Сервис отвечает через Service.
kubectl -n pulse-staging port-forward svc/pulse 18080:80 >/tmp/port-forward.log 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
wait_for "port-forward" curl -sf http://127.0.0.1:18080/healthz
body=$(curl -sf http://127.0.0.1:18080/)
echo "$body"
echo "$body" | grep -q '"environment":"staging"' || { echo "unexpected response"; exit 1; }
curl -sf http://127.0.0.1:18080/metrics | grep -q '^pulse_build_info{version="e2e"} 1' || { echo "metrics missing"; exit 1; }
echo "ok: service responds, metrics exported"

# Self-heal: ручная правка в кластере откатывается к состоянию из Git.
kubectl -n pulse-staging scale deployment/pulse --replicas=1
wait_for "self-heal restored replicas" bash -c "[ \"\$(kubectl -n pulse-staging get deployment pulse -o jsonpath='{.spec.replicas}')\" = 2 ]"

# Production не синхронизируется сам: это ручной promotion gate.
sleep 20
prod_sync=$(app_field pulse-production '{.status.sync.status}')
echo "pulse-production sync status: $prod_sync"
[ "$prod_sync" = OutOfSync ] || { echo "production must wait for manual sync"; exit 1; }
if kubectl get namespace pulse-production >/dev/null 2>&1 && kubectl -n pulse-production get deployment pulse >/dev/null 2>&1; then
  echo "production was deployed without manual sync"; exit 1
fi
echo "ok: production waits for manual sync"
echo "e2e passed"
