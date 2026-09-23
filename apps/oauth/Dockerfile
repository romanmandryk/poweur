# The Poweur OAuth/OIDC bridge (EPIC-022). Built from the repo root, because
# packages/identity is a replace-path sibling:
#   docker build -f apps/oauth/Dockerfile -t poweur-oauth .
FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY packages/identity ./packages/identity
COPY apps/oauth ./apps/oauth
WORKDIR /src/apps/oauth
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /poweur-oauth ./cmd/poweur-oauth

FROM alpine:3.20
ARG VERSION_HASH=
ARG BUILD_TIME=
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 oauth \
 && mkdir /data && chown oauth /data
COPY --from=builder /poweur-oauth /usr/local/bin/poweur-oauth
USER oauth
ENV OAUTH_ADDR=:8090 \
    OAUTH_DATABASE=/data/oauth.db \
    VERSION_HASH=${VERSION_HASH} \
    BUILD_TIME=${BUILD_TIME}
VOLUME /data
EXPOSE 8090
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8090/health >/dev/null || exit 1
ENTRYPOINT ["poweur-oauth"]
CMD ["serve"]
