FROM golang:1.20-buster
LABEL os=linux
LABEL arch=arm64

ENV GOOS=linux
ENV GOARCH=arm64
ENV CGO_ENABLED=1
ENV CC=aarch64-linux-gnu-gcc
ENV PATH="/go/bin/${GOOS}_${GOARCH}:${PATH}"
ENV PKG_CONFIG_PATH=/usr/lib/aarch64-linux-gnu/pkgconfig

# install build & runtime dependencies
RUN dpkg --add-architecture arm64 \
    && apt update \
    && apt install -y --no-install-recommends \
        protobuf-compiler \
        upx \
        gcc-aarch64-linux-gnu \
        libc6-dev-arm64-cross \
        pkg-config \
        libgtk-3-dev:arm64 \
        libappindicator3-dev:arm64 \
    && rm -rf /var/lib/apt/lists/*
