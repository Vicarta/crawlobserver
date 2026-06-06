# syntax=docker/dockerfile:1

FROM node:22-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.25.7-alpine AS backend
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/frontend/dist ./internal/server/frontend/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X github.com/SEObserver/crawlobserver/internal/updater.Version=${VERSION}" \
    -o /out/crawlobserver ./cmd/crawlobserver

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata chromium \
    && addgroup -S crawlobserver \
    && adduser -S -G crawlobserver -h /var/lib/crawlobserver crawlobserver
COPY --from=backend /out/crawlobserver /usr/local/bin/crawlobserver
WORKDIR /var/lib/crawlobserver
ENV CRAWLOBSERVER_CHROME_BIN=/usr/bin/chromium-browser
USER crawlobserver
EXPOSE 8899
ENTRYPOINT ["crawlobserver"]
CMD ["serve", "--config", "/etc/crawlobserver/config.yaml"]
