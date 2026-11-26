#!/usr/bin/env bash
# Статическая проверка чартов: lint, рендер всех окружений, схемы Kubernetes, отрицательные тесты.
set -euo pipefail
cd "$(dirname "$0")/.."

K8S_VERSION="${K8S_VERSION:-1.33.0}"
CRD_SCHEMAS='https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json'
OUT="${OUT:-rendered}"
mkdir -p "$OUT"

helm lint --strict charts/pulse
helm lint --strict gitops
for env in environments/*/; do
  env=$(basename "$env")
  helm lint --strict charts/pulse -f "environments/$env/values.yaml"
  helm template pulse charts/pulse -n "pulse-$env" -f "environments/$env/values.yaml" > "$OUT/pulse-$env.yaml"
done
# Все необязательные компоненты включены — чтобы проверить и их шаблоны.
helm template pulse charts/pulse --set serviceMonitor.enabled=true --set autoscaling.enabled=true \
  --set ingress.enabled=true --set ingress.tls.enabled=true --set ingress.tls.secretName=tls > "$OUT/pulse-all.yaml"
helm template platform gitops -n argocd > "$OUT/platform-apps.yaml"

kubeconform -strict -summary -kubernetes-version "$K8S_VERSION" \
  -schema-location default -schema-location "$CRD_SCHEMAS" "$OUT"/*.yaml

# Отрицательные тесты: эти значения чарт обязан отвергнуть.
expect_fail() {
  local name=$1; shift
  if helm template pulse charts/pulse "$@" >/dev/null 2>&1; then
    echo "FAIL: chart accepted invalid values ($name)"; exit 1
  fi
  echo "ok: rejected $name"
}
expect_fail "unknown key"           --set unknownKey=1
expect_fail "bad digest"            --set image.digest=sha256:123
expect_fail "zero replicas"         --set replicaCount=0
expect_fail "privileged port"       --set containerPort=80
expect_fail "max < min replicas"    --set autoscaling.enabled=true --set autoscaling.minReplicas=5 --set autoscaling.maxReplicas=2
expect_fail "tls without secret"    --set ingress.enabled=true --set ingress.tls.enabled=true
expect_fail "bad pull policy"       --set image.pullPolicy=Sometimes

# Ключевые требования безопасности — прямо в отрендеренном Deployment.
for f in "$OUT"/pulse-*.yaml; do
  for want in "runAsNonRoot: true" "readOnlyRootFilesystem: true" "allowPrivilegeEscalation: false" \
              "automountServiceAccountToken: false" "type: RuntimeDefault"; do
    grep -q "$want" "$f" || { echo "FAIL: $f lacks '$want'"; exit 1; }
  done
done
echo "chart validation passed"
