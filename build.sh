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

gobuild() {
  name="$1"
  for arch in 386 amd64 arm arm64; do
    filename="$name-linux-$arch-$VERSION"
    GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-X github.com/anywherelan/awl/config.Version=${VERSION}" -o "$filename"
    mv "$filename" "$builddir"
  done

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
  gobuild awl
}

build-awl-tray() {
  cd "$awldir/cmd/awl-tray"
  gobuild awl-tray
}

build-desktop() {
  build-awl
  build-awl-tray
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
clean)
  clean
  ;;
*)
  echo "unknown command '$@'"
  ;;
esac