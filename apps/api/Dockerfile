FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /relay .

FROM alpine:3.20
ARG VERSION=dev
ENV VERSION=${VERSION}
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /relay /relay
EXPOSE 8080
CMD ["/relay"]
