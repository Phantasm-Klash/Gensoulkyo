from heroiclabs/nakama-pluginbuilder:3.39.0 as builder

arg ALL_PROXY
arg HTTPS_PROXY
arg HTTP_PROXY
arg all_proxy
arg https_proxy
arg http_proxy
arg GOPROXY=https://goproxy.cn,direct
arg GOSUMDB=off

env ALL_PROXY=${ALL_PROXY}
env HTTPS_PROXY=${HTTPS_PROXY}
env HTTP_PROXY=${HTTP_PROXY}
env all_proxy=${all_proxy}
env https_proxy=${https_proxy}
env http_proxy=${http_proxy}
env GOPROXY=${GOPROXY}
env GOSUMDB=${GOSUMDB}

workdir /src

copy go.mod go.sum ./
run go mod download

copy runtime ./runtime
run mkdir -p /runtime-artifacts && \
    go build --trimpath --buildmode=plugin -o /runtime-artifacts/gensoulkyo.so ./runtime
