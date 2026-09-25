# Built from the repo root (identity is a replace-path sibling of apps/api):
#   docker build -f apps/api/Dockerfile \
#     --build-arg VERSION_HASH=$(git rev-parse HEAD) \
#     --build-arg BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ) .
FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY packages/identity ./packages/identity
COPY apps/api ./apps/api
WORKDIR /src/apps/api
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /relay .

# The web client (apps/web), served at /app/. Built here rather than committed,
# because prod has no Node toolchain — the deploy only pulls this image.
FROM node:22-alpine AS web
RUN npm install -g pnpm@10
WORKDIR /src
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY packages/client-ts ./packages/client-ts
COPY packages/faro ./packages/faro
COPY apps/web ./apps/web
RUN pnpm install --frozen-lockfile --filter @poweur/web...
RUN pnpm --filter @poweur/client build && pnpm --filter @poweur/web build

FROM alpine:3.20
ARG VERSION_HASH=
ARG BUILD_TIME=
ENV VERSION_HASH=${VERSION_HASH}
ENV BUILD_TIME=${BUILD_TIME}
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /relay /relay
COPY --from=web /src/apps/web/dist /web
# Anyone running the released image gets the same app at /app/.
ENV WEB_STATIC_DIR=/web
EXPOSE 8080
CMD ["/relay"]
