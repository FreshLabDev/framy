# SPDX-License-Identifier: Apache-2.0
FROM golang:1.27.3-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/framy ./cmd/framy

FROM alpine:3.24

# ffmpeg normalizes the photos people bring: HEIC, AVIF, WebP, JPEG, PNG, and
# encodes the loops of internal/anim with libx264.
# Alpine 3.24 ships FFmpeg 8.1, whose scale filter converts Display P3.
RUN apk add --no-cache ca-certificates tzdata ffmpeg \
    && adduser -D -H -u 10001 framy

WORKDIR /app

# Migrations and the catalog files are embedded in the binary; there is
# nothing else to copy.
COPY --from=build /out/framy /app/framy

# docker inspect answers which build is running without starting it.
ARG VERSION=dev
ARG COMMIT=none
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.source="https://github.com/FreshLabDev/framy"
USER framy

EXPOSE 8080

ENTRYPOINT ["/app/framy"]
