# SPDX-License-Identifier: Apache-2.0
# Both images are pinned by digest as well as by tag, so a build starts from
# the same files until Dependabot proposes newer ones. Alpine's packages are
# not pinned: its repository keeps only the newest of each, and a pin would
# break the build on the day a fix lands. The release tests and scans the
# image it publishes instead (docs/releases.md).
FROM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build

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

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf

# ffmpeg normalizes the photos people bring: HEIC, AVIF, WebP, JPEG, PNG, and
# encodes the loops of internal/anim with libx264.
# Alpine 3.24 ships FFmpeg 8.1, whose scale filter converts Display P3.
# The upgrade brings the packages of the pinned base up to the fixes Alpine
# has published since, as the added ones are.
RUN apk upgrade --no-cache \
    && apk add --no-cache ca-certificates tzdata ffmpeg \
    && adduser -D -H -u 10001 framy \
    && install -d -o framy -g framy /var/lib/framy/work /var/lib/framy/anim

# Where a stack mounts its volumes for WORK_DIR and ANIM_CACHE_DIR. Owned by
# framy here, so a new named volume mounted on them is framy's too; a
# volume mounted on a path the image lacks would be root's, and the service
# could not write to it.

WORKDIR /app

# Migrations and the catalog files are embedded in the binary; there is
# nothing else to copy.
COPY --from=build /out/framy /app/framy

# docker inspect answers which build is running without starting it.
ARG VERSION=dev
ARG COMMIT=none
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.source="https://github.com/asterfield-dev/framy"
USER framy

EXPOSE 8080

ENTRYPOINT ["/app/framy"]
