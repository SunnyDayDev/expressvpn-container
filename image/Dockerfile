# Контекст сборки — корень репозитория (см. docker-compose.yml).

# ---------- Веб-интерфейс (Svelte + Vite) ----------
FROM node:22-alpine AS web-builder
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---------- Агент (Go, статический бинарь; статика UI через go:embed) ----------
FROM golang:1.25-trixie AS agent-builder
WORKDIR /src
COPY image/agent/go.mod image/agent/go.sum ./
RUN go mod download
COPY image/agent/ ./
COPY --from=web-builder /web/dist/ ./internal/webui/dist/
ARG AGENT_VERSION=0.1.0
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${AGENT_VERSION}" \
    -o /out/detour-agent ./cmd/detour-agent

# ---------- Рантайм ----------
FROM debian:trixie-slim

ENV DEBIAN_FRONTEND=noninteractive
# Зависимости демона ExpressVPN (см. docs/spikes/S1.md) + nftables/iproute2 для
# kill switch и маршрутов агента, curl для healthcheck.
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash ca-certificates curl iproute2 iptables nftables procps psmisc \
    libatomic1 libbrotli1 libnl-3-200 libnl-route-3-200 libglib2.0-0 libcap2-bin \
    libdbus-1-3 \
    && rm -rf /var/lib/apt/lists/*

# ---------- ExpressVPN: официальный инсталлятор ----------
# Обычно качается при сборке; на медленных/ограниченных каналах (NAS) файл
# можно положить заранее в .build-cache/ в корне репозитория — тогда сборка
# возьмёт его оттуда. Контрольная сумма проверяется в обоих случаях.
ARG EXPRESSVPN_VERSION=14.2.0.13656
ARG EXPRESSVPN_SHA256=9d770edc6548a17994fd15714c5030a8f5767671b76b2117a884f7d48d93f2cb
COPY .build-cache/ /tmp/build-cache/
RUN set -eu; \
    cached="/tmp/build-cache/expressvpn-linux-universal-${EXPRESSVPN_VERSION}_release.run"; \
    url="https://www.expressvpn.works/clients/linux/expressvpn-linux-universal-${EXPRESSVPN_VERSION}_release.run"; \
    if [ -f "$cached" ]; then \
        echo "Using cached installer $cached"; \
        cp "$cached" /tmp/expressvpn.run; \
    else \
        echo "Downloading ${url}"; \
        curl -fSL -o /tmp/expressvpn.run "$url" \
            || { echo "ERROR: failed to download ExpressVPN installer: ${url}"; exit 1; }; \
    fi; \
    echo "${EXPRESSVPN_SHA256}  /tmp/expressvpn.run" | sha256sum -c - \
        || { echo "ERROR: checksum mismatch for ${url} (expected ${EXPRESSVPN_SHA256})"; exit 1; }; \
    # --force-dependencies: пропускает проверку NET_ADMIN/tun (их нет на этапе сборки);
    # без --sysvinit/--systemd инсталлятор в контейнере не настраивает сервис —
    # демоном управляет агент напрямую (docs/spikes/S1.md)
    sh /tmp/expressvpn.run --accept --quiet --nox11 --noprogress -- --no-gui --force-dependencies; \
    rm -rf /tmp/expressvpn.run /tmp/build-cache

# ---------- sing-box (uplink-движок) ----------
ARG TARGETARCH
ARG SINGBOX_VERSION=1.13.19
ARG SINGBOX_SHA256_ARM64=7fe3597a95a3c5ad67477b1d7653b9ce097e0be7c676758eba1fcf558f353d57
ARG SINGBOX_SHA256_AMD64=ef88a9e577d474210867bd708933d042e9b70106529df2656182c9db90106aa1
RUN set -eu; \
    # TARGETARCH заполняет только BuildKit; классический builder (NAS со
    # старым Docker) оставляет его пустым — определяем сами.
    arch="${TARGETARCH:-}"; \
    if [ -z "$arch" ]; then \
        case "$(uname -m)" in \
            x86_64) arch=amd64 ;; \
            aarch64|arm64) arch=arm64 ;; \
        esac; \
    fi; \
    case "$arch" in \
        arm64) sha="${SINGBOX_SHA256_ARM64}" ;; \
        amd64) sha="${SINGBOX_SHA256_AMD64}" ;; \
        *) echo "ERROR: unsupported architecture '${arch:-unknown}'"; exit 1 ;; \
    esac; \
    url="https://github.com/SagerNet/sing-box/releases/download/v${SINGBOX_VERSION}/sing-box-${SINGBOX_VERSION}-linux-${arch}.tar.gz"; \
    echo "Downloading ${url}"; \
    curl -fSL -o /tmp/sing-box.tgz "$url" \
        || { echo "ERROR: failed to download sing-box: ${url}"; exit 1; }; \
    echo "${sha}  /tmp/sing-box.tgz" | sha256sum -c - \
        || { echo "ERROR: checksum mismatch for ${url} (expected ${sha})"; exit 1; }; \
    tar -xzf /tmp/sing-box.tgz -C /tmp; \
    install -m 755 "/tmp/sing-box-${SINGBOX_VERSION}-linux-${arch}/sing-box" /usr/local/bin/sing-box; \
    rm -rf /tmp/sing-box.tgz "/tmp/sing-box-${SINGBOX_VERSION}-linux-${arch}"

COPY --from=agent-builder /out/detour-agent /usr/local/bin/detour-agent

# Версии компонентов — агент отдаёт их в GET /v1/version
ARG AGENT_VERSION=0.1.0
# LD_LIBRARY_PATH здесь намеренно НЕ выставляется глобально: библиотеки
# ExpressVPN (свой старый libssl) ломают системные бинарники (curl и пр.).
# Агент передаёт LD_LIBRARY_PATH=/opt/expressvpn/lib только процессу демона.
ENV DETOUR_AGENT_VERSION=${AGENT_VERSION} \
    DETOUR_EXPRESSVPN_VERSION=${EXPRESSVPN_VERSION} \
    DETOUR_SINGBOX_VERSION=${SINGBOX_VERSION}

VOLUME /data
# 1080 — входящий SOCKS5, 8080 — веб-интерфейс + API
EXPOSE 1080 8080

HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=3 \
    CMD ["/usr/local/bin/detour-agent", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/detour-agent"]
