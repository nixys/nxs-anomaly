#!/usr/bin/env bash
# Run from the public Community checkout root. Never rotate an existing database implicitly.
set -euo pipefail
release_tag="${1:-v$(cat VERSION)}"
[[ "$release_tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$ ]] || { echo 'Pass a release tag, e.g. v0.1.107' >&2; exit 1; }
[ -f frontend/nginx.conf.template ] || { echo 'Run from the checkout root' >&2; exit 1; }
[ ! -e .local/credentials.env ] || { echo 'Existing credentials found; reuse .local/ rather than regenerate passwords' >&2; exit 1; }
umask 077
mkdir -p .local
cat > .local/credentials.env <<EOF
POSTGRES_PASSWORD=$(openssl rand -hex 24)
NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME=admin
NXS_ANOMALY_BOOTSTRAP_ADMIN_PASSWORD=$(openssl rand -hex 24)
NXS_ANOMALY_API_KEY=$(openssl rand -hex 32)
EOF
. .local/credentials.env
cp deploy/quickstart/delivery.env.example .local/delivery.env
# Only substitute our generated, shell-safe values; no evaluation of a template.
for preset in on-premise kubernetes compose; do
  sed -e "s/\${POSTGRES_PASSWORD}/${POSTGRES_PASSWORD}/g" \
      -e "s/\${RELEASE_TAG}/${release_tag}/g" \
      "deploy/quickstart/${preset}.env.example" > ".local/${preset}.env"
done
cp deploy/quickstart/compose.yaml .local/compose.yaml
cp frontend/nginx.conf.template .local/frontend.nginx.conf.template
chmod 600 .local/*.env
echo 'Prepared .local/ configuration. Configure a transport in delivery.env before testing notifications.'
