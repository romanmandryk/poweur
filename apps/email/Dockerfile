# The Poweur email bridge (EPIC-023). Built from the repo root, because
# packages/identity is a replace-path sibling:
#   docker build -f apps/email/Dockerfile -t poweur-email .
FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY packages/identity ./packages/identity
COPY apps/email ./apps/email
WORKDIR /src/apps/email
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /poweur-email ./cmd/poweur-email

FROM alpine:3.20
ARG VERSION_HASH=
ARG BUILD_TIME=
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10002 email \
 && mkdir /data && chown email /data
COPY --from=builder /poweur-email /usr/local/bin/poweur-email
USER email
ENV EMAIL_ADDR=:8091 \
    EMAIL_LMTP_ADDR=:2525 \
    EMAIL_DATA=/data \
    VERSION_HASH=${VERSION_HASH} \
    BUILD_TIME=${BUILD_TIME}
VOLUME /data
EXPOSE 8091 2525
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8091/health >/dev/null || exit 1
ENTRYPOINT ["poweur-email"]
CMD ["serve"]
