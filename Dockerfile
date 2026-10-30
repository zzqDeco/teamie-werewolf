# syntax=docker/dockerfile:1.7

FROM node:22-alpine AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.24-alpine AS go-builder
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/embed.go ./web/embed.go
COPY --from=web-builder /src/web/dist ./web/dist

ARG APP_VERSION=0.1.0
ARG GIT_SHA=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.buildVersion=${APP_VERSION} -X main.buildSHA=${GIT_SHA}" \
    -o /out/werewolf ./cmd/werewolf

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata wget \
    && adduser -D -u 10001 werewolf
USER werewolf
WORKDIR /app
COPY --from=go-builder /out/werewolf /app/werewolf
EXPOSE 8080
ENTRYPOINT ["/app/werewolf"]
