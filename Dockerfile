# syntax=docker/dockerfile:1

# ---- Build stage ----------------------------------------------------------
FROM golang:1.22-bookworm AS build

WORKDIR /src
COPY go.mod ./
# No third-party dependencies: go.sum is not required.
RUN go mod download || true
COPY . .

# Build a static binary so it runs in a minimal final image.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/roled ./cmd/roled

# ---- Final stage ----------------------------------------------------------
# Debian slim so we can install the official Ookla speedtest CLI from apt.
FROM debian:bookworm-slim

# Install the official Ookla speedtest CLI from their apt repository.
# The speed_check role invokes it with --accept-license --accept-gdpr, so no
# interactive license prompt is required at runtime.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl gnupg \
    && curl -s https://packagecloud.io/install/repositories/ookla/speedtest-cli/script.deb.sh | bash \
    && apt-get install -y --no-install-recommends speedtest \
    && apt-get purge -y curl gnupg \
    && apt-get autoremove -y \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/roled /usr/local/bin/roled
COPY config.example.json /etc/roled/config.json

# Secrets are provided at runtime via environment variables referenced by each
# channel's password_env (for example SMTP_PASS, SMSGATE_PASS). Never bake
# secrets into the image.
ENTRYPOINT ["/usr/local/bin/roled"]
CMD ["-config", "/etc/roled/config.json"]
