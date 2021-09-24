#!/bin/bash

awldir=$(pwd)
builddir="$awldir/build"
awlflutterdir="$awldir/../awl-flutter"

# until https://github.com/golang/go/issues/37475 is implemented
VERSION=$(git describe --tags --always --abbrev=8 --dirty)

wintun_version="wintun-0.11"
if [[ ! -e "/tmp/$wintun_version" ]]; then
  wget "https://www.wintun.net/builds/$wintun_version.zip"
  unzip "$wintun_version.zip" -d "/tmp/$wintun_version"
  rm -f "$wintun_version.zip"
fi

gobuild-linux() {
  name="$1"
  for arch in 386 amd64 arm arm64; do
    filename="$name-linux-$arch-$VERSION"
    GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-X github.com/anywherelan/awl/config.Version=${VERSION}" -o "$filename"
    mv "$filename" "$builddir"
  done
}

gobuild-windows() {
  name="$1"
  for tuple in "386 x86" "amd64 amd64"; do
    goarch=$(echo "$tuple" | cut -f1 -d" ")
    wintunarch=$(echo "$tuple" | cut -f2 -d" ")
    cp "/tmp/$wintun_version/wintun/bin/$wintunarch/wintun.dll" wintun.dll

    filename="$name-windows-$goarch-$VERSION.exe"
    GOOS=windows GOARCH=$goarch go build -trimpath -ldflags "-H windowsgui -X github.com/anywherelan/awl/config.Version=${VERSION}" -o "$filename"
    mv "$filename" "$builddir"
    rm -f "wintun.dll"
  done
}

# Commands

clean() {
  rm -rf build/
  mkdir build
  rm -rf static/
}

build-web() {
  cd "$awldir"
  rm -rf static/
  cd "$awlflutterdir"
  flutter build web --release
  cp -r "$awlflutterdir/build/web" "$awldir/static"
}

build-mobile-lib() {
  cd "$awldir/cmd/gomobile-lib"
  go get -d golang.org/x/mobile/cmd/gomobile
  gomobile bind -trimpath -ldflags "-X github.com/anywherelan/awl/config.Version=${VERSION}" -o anywherelan.aar -target=android .
  go mod edit -droprequire=golang.org/x/mobile
  go mod tidy
  mv anywherelan.aar "$awlflutterdir/android/app/src/main/libs/"
}

build-mobile-apk() {
  cd "$awlflutterdir"
  flutter build apk --release
  mv "$awlflutterdir/build/app/outputs/flutter-apk/app-release.apk" "$builddir/awl-android-multiarch-$VERSION.apk"
}

build-mobile() {
  build-mobile-lib
  build-mobile-apk
}

build-awl() {
  cd "$awldir/cmd/awl"
  gobuild-linux awl
  gobuild-windows awl
}

build-awl-tray() {
  cd "$awldir/cmd/awl-tray"
  gobuild-windows awl-tray
  build-awl-tray-linux-crosscompile
}

build-awl-tray-linux() {
  goos="$(go env GOOS)"
  arch="$(go env GOARCH)"
  filename="awl-tray-$goos-$arch-$VERSION"
  cd "$awldir/cmd/awl-tray"
  go build -trimpath -ldflags "-X github.com/anywherelan/awl/config.Version=${VERSION}" -o "$filename"
  # set host's rights because when build from docker it will be root:root
  host_uid="$(stat -c "%u" "$builddir")"
  host_gid="$(stat -c "%g" "$builddir")"
  chown "$host_uid:$host_gid" "$filename"
  mv "$filename" "$builddir"
}

build-awl-tray-linux-crosscompile() {
  cd "$awldir"
  for arch in 386 amd64 arm arm64; do
    docker run --rm -v "$PWD":/usr/src/myapp -w /usr/src/myapp "awl-cross-$arch" /bin/sh -c './build.sh awl-tray-linux'
  done
}

build-desktop() {
  build-awl
  build-awl-tray
}

build-docker-images() {
  for arch in 386 amd64 arm arm64; do
    docker build -t "awl-cross-$arch" "./crosscompile/linux-$arch"
  done
}

case "${1:-default}" in
release)
  clean
  build-web
  build-mobile
  build-desktop
  ;;
web)
  build-web
  ;;
mobile-lib)
  build-mobile-lib
  ;;
mobile)
  build-mobile
  ;;
desktop)
  build-desktop
  ;;
awl-tray-linux-crosscompile)
  build-awl-tray-linux-crosscompile
  ;;
awl-tray-linux)
  build-awl-tray-linux
  ;;
docker-images)
  build-docker-images
  ;;
clean)
  clean
  ;;
*)
  echo "unknown command '$@'"
  ;;
esac
