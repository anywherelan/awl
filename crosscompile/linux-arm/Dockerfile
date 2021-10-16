FROM golang:1.17-buster
LABEL os=linux
LABEL arch=armhf

ENV GOOS=linux
ENV GOARCH=arm
ENV CGO_ENABLED=1
ENV CC=arm-linux-gnueabihf-gcc
ENV PATH="/go/bin/${GOOS}_${GOARCH}:${PATH}"
ENV PKG_CONFIG_PATH=/usr/lib/arm-linux-gnueabihf/pkgconfig

# install build & runtime dependencies
RUN dpkg --add-architecture armhf \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        protobuf-compiler \
        upx \
        gcc-arm-linux-gnueabihf \
        libc6-dev-armhf-cross \
        pkg-config \
        libgtk-3-dev:armhf \
        libappindicator3-dev:armhf \
    && rm -rf /var/lib/apt/lists/*
