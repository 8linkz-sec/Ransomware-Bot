# Build Stage
ARG GO_BASE_IMAGE=golang:1.27-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125
ARG RUNTIME_BASE_IMAGE=alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
FROM ${GO_BASE_IMAGE} AS builder

ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ARG ALPINE_REPOSITORY_BASE=
ENV GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}

# Install build dependencies
RUN if [ -n "${ALPINE_REPOSITORY_BASE}" ]; then \
    printf '%s\n%s\n' "${ALPINE_REPOSITORY_BASE}/main" "${ALPINE_REPOSITORY_BASE}/community" > /etc/apk/repositories; \
    fi && \
    apk add --no-cache git ca-certificates tzdata

WORKDIR /build

# Copy go mod files for better layer caching
COPY go.mod go.sum ./
RUN go mod download && go mod verify

# Copy only source needed for compilation. Runtime config is mounted, not built in.
COPY main.go ./
COPY internal ./internal

# Build with optimizations and security hardening
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=1.2.0
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -a \
    -ldflags="-w -s -extldflags '-static' \
    -X main.Version=${VERSION} \
    -X main.Commit=${COMMIT} \
    -X main.BuildDate=${BUILD_DATE}" \
    -trimpath \
    -o ransomware-news-bot \
    .

# Runtime Stage
FROM ${RUNTIME_BASE_IMAGE}

ARG VERSION=1.2.0
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
ARG ALPINE_REPOSITORY_BASE=
ARG APP_DIR=/app
ARG BOT_USER=botuser
ARG BOT_GROUP=botuser
ARG BOT_UID=1000
ARG BOT_GID=1000
ARG APP_READ_MODE=755
ARG APP_PRIVATE_MODE=750
LABEL org.opencontainers.image.title="ransomware-news-bot" \
    org.opencontainers.image.description="Ransomware and RSS alert delivery bot for Discord and Slack" \
    org.opencontainers.image.version="${VERSION}" \
    org.opencontainers.image.revision="${COMMIT}" \
    org.opencontainers.image.created="${BUILD_DATE}" \
    org.opencontainers.image.source="https://github.com/8linkz-sec/Ransomware-Bot"

# Install runtime dependencies
RUN if [ -n "${ALPINE_REPOSITORY_BASE}" ]; then \
    printf '%s\n%s\n' "${ALPINE_REPOSITORY_BASE}/main" "${ALPINE_REPOSITORY_BASE}/community" > /etc/apk/repositories; \
    fi && \
    apk add --no-cache \
    ca-certificates \
    tzdata

WORKDIR ${APP_DIR}

# Create non-root user with operator-overridable UID/GID for consistency.
RUN addgroup -g ${BOT_GID} ${BOT_GROUP} && \
    adduser -D -u ${BOT_UID} -G ${BOT_GROUP} -s /sbin/nologin ${BOT_USER}

# Copy binary from builder. Keep the application root and executable root-owned.
COPY --from=builder /build/ransomware-news-bot ${APP_DIR}/

# Create directories with proper permissions
RUN mkdir -p ${APP_DIR}/logs ${APP_DIR}/data ${APP_DIR}/configs && \
    chown root:root ${APP_DIR} ${APP_DIR}/ransomware-news-bot ${APP_DIR}/configs && \
    chmod ${APP_READ_MODE} ${APP_DIR} ${APP_DIR}/ransomware-news-bot ${APP_DIR}/configs && \
    chown -R ${BOT_USER}:${BOT_GROUP} ${APP_DIR}/logs ${APP_DIR}/data && \
    chmod ${APP_PRIVATE_MODE} ${APP_DIR}/logs ${APP_DIR}/data

# Switch to non-root user
USER ${BOT_USER}

# Define volumes for persistence (after USER to preserve ownership)
VOLUME ["${APP_DIR}/configs", "${APP_DIR}/logs", "${APP_DIR}/data"]

# Set environment variables. data_dir is read from runtime config unless DATA_DIR
# is explicitly provided by the operator. Docker healthcheck requires the
# scheduler readiness marker written after initial API/RSS checks complete.
ENV TZ=UTC \
    RANSOMWARE_BOT_READY_FILE=/tmp/ransomware-bot.ready

# Health check: validate scheduler readiness, runtime state, and terminal delivery failures.
HEALTHCHECK --interval=60s --timeout=5s --start-period=30s --retries=3 \
    CMD ["./ransomware-news-bot", "--healthcheck", "--config-dir", "./configs"]

# Start the bot. Keep the binary as ENTRYPOINT so docker run image --flag and
# docker compose run service --flag invoke the CLI instead of replacing it.
ENTRYPOINT ["./ransomware-news-bot"]
CMD ["--config-dir", "./configs"]
