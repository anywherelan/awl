#!/bin/bash

awldir=$(pwd)
builddir="$awldir/build"
awlflutterdir="$awldir/../awl-flutter"
tempdir=$(dirname $(mktemp -u))

wintun_version="wintun-0.14.1"

# until https://github.com/golang/go/issues/37475 is implemented
VERSION=$(git describe --tags --always --abbrev=8 --dirty)

# download dependencies
download-wintun() {
  echo "check dependencies"

  goos="$(go env GOOS)"
  local force_download="$1"
  if [ "$force_download" != "true" ] && [ "$goos" != "windows" ]; then
    echo "dependencies loaded successfully"
    return
  fi

  if [[ ! -e "$tempdir/$wintun_version" ]]; then
    download_url="https://www.wintun.net/builds/$wintun_version.zip"
    if type "wget" >/dev/null; then
      wget "$download_url"
    elif type "curl" >/dev/null; then
      curl -sSL "$download_url" >"$wintun_version.zip"
    else
      echo "wget or curl could not be found. Please install it"
      exit 1
    fi

    unzip "$wintun_version.zip" -d "$tempdir/$wintun_version"
    rm -f "$wintun_version.zip"
  fi

  echo "dependencies loaded successfully"
}

install-wintun() {
  if [[ ! -e "$tempdir/$wintun_version" ]]; then
    return
  fi
  arch="$1"
  wintunarch="$arch"
  if [ "$arch" == "386" ]; then
    wintunarch="x86"
  fi
  cp "$tempdir/$wintun_version/wintun/bin/$wintunarch/wintun.dll" "$awldir/embeds/wintun.dll"
}

# build for linux OS
gobuild-linux() {
  name="$1"
  for arch in 386 amd64 arm arm64 mips mipsle; do
    archive_name="$name-linux-$arch-$VERSION.tar.gz"
    filename="$name"
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w -X github.com/anywherelan/awl/config.Version=${VERSION}" -o "$filename"
    tar -czf "$archive_name" "$filename"
    rm "$filename"
    mv "$archive_name" "$builddir"
  done
}

# build for macOS
gobuild-macos() {
  name="$1"
  for arch in amd64 arm64; do
    archive_name="$name-macos-$arch-$VERSION.zip"
    filename="$name"
    CGO_ENABLED=1 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "-s -w -X github.com/anywherelan/awl/config.Version=${VERSION}" -o "$filename"
    zip "$archive_name" "$filename"
    rm "$filename"
    mv "$archive_name" "$builddir"
  done
}

# build for windows OS
gobuild-windows() {
  name="$1"
  for arch in 386 amd64; do
    install-wintun "$arch"
    archive_name="$name-windows-$arch-$VERSION.zip"
    filename="$name.exe"
    CGO_ENABLED=0 GOOS=windows GOARCH=$arch go build -trimpath -ldflags "-s -w -H windowsgui -X github.com/anywherelan/awl/config.Version=${VERSION}" -o "$filename"
    zip "$archive_name" "$filename"
    rm "$filename"
    mv "$archive_name" "$builddir"
  done
}

# Commands (functions with "cross" build for different arch-es/OS-es)

# create new build dir, delete static dir
clean() {
  rm -rf build/
  mkdir build
  rm -rf static/
}

# build flutter web
build-web() {
  cd "$awldir"
  rm -rf static/
  cd "$awlflutterdir"
  flutter build web --release
  cp -r "$awlflutterdir/build/web" "$awldir/static"
  cd "$awldir"
  # at the time of flutter 3.3, canvaskit is saved in build files, but still not used in release run (using CDN instead)
  # so we don't need extra 15 MB in our binaries
  # see https://stackoverflow.com/q/70747972 with answer from core team
  rm -rf static/canvaskit
}

# build android library
build-android-lib() {
  cd "$awldir/cmd/gomobile-lib"
  go get golang.org/x/mobile/cmd/gomobile
  # about `-checklinkname=0` https://github.com/wlynxg/anet#how-to-build-with-go-1230-or-later
  gomobile bind -trimpath -ldflags "-s -w -checklinkname=0 -X github.com/anywherelan/awl/config.Version=${VERSION}" -o anywherelan.aar -target=android .
  go mod edit -droprequire=golang.org/x/mobile
  go mod tidy -compat=1.24
  mkdir -p "$awlflutterdir/android/app/src/main/libs"
  mv anywherelan.aar "$awlflutterdir/android/app/src/main/libs/"
}

# build for android, require android lib
build-android-apk() {
  cd "$awlflutterdir"
  flutter build apk --release
  mv "$awlflutterdir/build/app/outputs/flutter-apk/app-release.apk" "$builddir/awl-android-$VERSION.apk"
}

# build for android
build-android() {
  build-android-lib
  build-android-apk
}

# build server version
build-awl-cross() {
  cd "$awldir/cmd/awl"
  gobuild-linux awl
  gobuild-windows awl
}

# build desktop version for windows and others OS
build-awl-tray-cross() {
  cd "$awldir/cmd/awl-tray"
  gobuild-linux awl-tray
  gobuild-windows awl-tray
}

# build desktop version based on current environment
build-awl-tray() {
  goos="$(go env GOOS)"
  arch="$(go env GOARCH)"
  filename="awl-tray"
  archive_name="awl-tray-$goos-$arch-$VERSION"
  if [ "$goos" == "windows" ]; then
    install-wintun "$arch"
    filename="$filename.exe"
    archive_name="$archive_name.zip"
  elif [ "$goos" == "linux" ]; then
    archive_name="$archive_name.tar.gz"
  fi
  cd "$awldir/cmd/awl-tray"
  CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/anywherelan/awl/config.Version=${VERSION}" -o "$filename"
  if [ "$goos" == "windows" ]; then
    zip "$archive_name" "$filename"
  elif [ "$goos" == "linux" ]; then
    tar -czf "$archive_name" "$filename"
  fi
  rm "$filename"
  mv "$archive_name" "$builddir"
}

# build server and desktop versions
build-desktop-cross() {
  build-awl-cross
  build-awl-tray-cross
}

case "${1:-default}" in
release)
  clean
  download-wintun true
  build-web
  build-android
  build-desktop-cross
  ;;
release-macos)
  cd "$awldir/cmd/awl-tray"
  gobuild-macos awl-tray
  ;;
web)
  build-web
  ;;
android-lib)
  build-android-lib
  ;;
android)
  build-android
  ;;
awl-tray)
  download-wintun
  build-awl-tray
  ;;
deps)
  download-wintun
  install-wintun "$(go env GOARCH)"
  ;;
clean)
  clean
  ;;
*)
  echo "unknown command '$@'"
  ;;
esac
