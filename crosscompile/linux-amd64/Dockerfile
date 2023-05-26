FROM golang:1.20-buster
LABEL os=linux
LABEL arch=amd64

ENV GOOS=linux
ENV GOARCH=amd64
ENV CGO_ENABLED=1
ENV CC=gcc

# install build & runtime dependencies
RUN apt update \
    && apt install -y --no-install-recommends \
        protobuf-compiler \
        upx \
        pkg-config \
        libgtk-3-dev:amd64 \
        libappindicator3-dev:amd64 \
    && rm -rf /var/lib/apt/lists/*

RUN git config --global safe.directory '*'
