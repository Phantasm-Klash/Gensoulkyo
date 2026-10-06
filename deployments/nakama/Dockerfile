# Self-contained Gensoulkyo Nakama image: builds the Go runtime plugin with
# the pluginbuilder that matches the server Go toolchain, then bakes the
# resulting .so into the official Nakama server image.
#
# The Go plugin ABI requires the plugin and the server to be built with the
# same Go version, so the plugin MUST be built inside the matching
# heroiclabs/nakama-pluginbuilder image (never with a random local toolchain).
#
# Build context MUST be the workspace root that contains both `Gensoulkyo/`
# and `PhK-Protocol/` (the go.mod replace target), for example:
#
#   docker build -f Gensoulkyo/deployments/nakama/Dockerfile \
#     -t gensoulkyo-nakama:3.41.0 .
#
# or via docker compose (see docker-compose.build.yml).

ARG NAKAMA_VERSION=3.41.0

FROM heroiclabs/nakama-pluginbuilder:${NAKAMA_VERSION} AS builder

ARG NAKAMA_VERSION
ENV GO111MODULE=on \
    CGO_ENABLED=1 \
    GOPROXY=https://goproxy.cn,direct \
    GOSUMDB=off

WORKDIR /build
COPY PhK-Protocol/ /build/PhK-Protocol/
COPY Gensoulkyo/ /build/Gensoulkyo/
WORKDIR /build/Gensoulkyo

# -tags nakama enables the Nakama Go runtime binding in cmd/gensoulkyo_nakama.
RUN go build -tags nakama -trimpath -buildmode=plugin \
    -o /build/gensoulkyo.so ./cmd/gensoulkyo_nakama

FROM heroiclabs/nakama:${NAKAMA_VERSION}

ARG NAKAMA_VERSION
LABEL org.opencontainers.image.title="gensoulkyo-nakama"
LABEL org.opencontainers.image.version="${NAKAMA_VERSION}"

COPY --from=builder /build/gensoulkyo.so /nakama/data/modules/gensoulkyo.so
COPY Gensoulkyo/deployments/nakama/local.yml /nakama/data/local.yml
