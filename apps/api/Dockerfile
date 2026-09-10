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

FROM alpine:3.20
ARG VERSION_HASH=
ARG BUILD_TIME=
ENV VERSION_HASH=${VERSION_HASH}
ENV BUILD_TIME=${BUILD_TIME}
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /relay /relay
EXPOSE 8080
CMD ["/relay"]
