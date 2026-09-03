FROM node:22-alpine AS build

WORKDIR /app

# Dependencies first: this layer is reused by the registry build cache whenever
# package-lock.json is unchanged.
COPY package.json package-lock.json ./
RUN npm ci

COPY tsconfig.json vite.config.ts index.html ./
COPY src ./src
RUN npm run build

FROM nginx:1.27-alpine

ARG VERSION=dev
LABEL org.opencontainers.image.title="nxs-anomaly frontend" \
      org.opencontainers.image.version="${VERSION}"

# Only NXS_ANOMALY_* placeholders are substituted, so nginx's own $variables survive.
# The CSP is an env var, not a literal in the template, because it is the one
# security header a deployment may legitimately have to widen — an extra origin,
# an embedding parent — and a header baked into the image leaves no way to do
# that short of rebuilding it. The default describes what this build emits:
# hashed same-origin assets, no inline script, no third-party origin.
ENV NGINX_ENVSUBST_FILTER=NXS_ANOMALY_ \
    NXS_ANOMALY_API_UPSTREAM=app:8080 \
    NXS_ANOMALY_CSP="default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"
COPY --from=build /app/dist /usr/share/nginx/html
COPY nginx.conf.template /etc/nginx/templates/default.conf.template
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/index.html >/dev/null || exit 1
