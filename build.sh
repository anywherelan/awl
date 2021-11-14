#!/bin/bash

awldir=$(pwd)
builddir="$awldir/build"
awlflutterdir="$awldir/../awl-flutter"
tempdir=$(dirname $(mktemp -u))

wintun_version="wintun-0.11"


# until https://github.com/golang/go/issues/37475 is implemented
VERSION=$(git describe --tags --always --abbrev=8 --dirty)


# download dependencies
download-wintun() {
  echo "check dependencies"
  if [[ ! -e "$tempdir/$wintun_version" ]]; then
    if ! type "wget" > /dev/null; then
        echo "wget util could not be found. Please install it"
        exit
    fi
    wget "https://www.wintun.net/builds/$wintun_version.zip"
    unzip "$wintun_version.zip" -d "$tempdir/$wintun_version"
    rm -f "$wintun_version.zip"
  fi
  echo "dependencies are loaded successfully"
}

# build for linux OS
gobuild-linux() {
  name="$1"
  for arch in 386 amd64 arm arm64; do
    filename="$name-linux-$arch-$VERSION"
    GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w -X github.com/anywherelan/awl/config.Version=${VERSION}" -o "$filename"
    mv "$filename" "$builddir"
  done
}

# build for windows OS
gobuild-windows() {
  name="$1"
  for tuple in "386 x86" "amd64 amd64"; do
    goarch=$(echo "$tuple" | cut -f1 -d" ")
    wintunarch=$(echo "$tuple" | cut -f2 -d" ")
    cp "$tempdir/$wintun_version/wintun/bin/$wintunarch/wintun.dll" wintun.dll

    filename="$name-windows-$goarch-$VERSION.exe"
    GOOS=windows GOARCH=$goarch go build -trimpath -ldflags "-s -w -H windowsgui -X github.com/anywherelan/awl/config.Version=${VERSION}" -o "$filename"
    mv "$filename" "$builddir"
    rm -f "wintun.dll"
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
}

# build mobile library
build-mobile-lib() {
  cd "$awldir/cmd/gomobile-lib"
  go get -d golang.org/x/mobile/cmd/gomobile
  gomobile bind -trimpath -ldflags "-s -w -X github.com/anywherelan/awl/config.Version=${VERSION}" -o anywherelan.aar -target=android .
  go mod edit -droprequire=golang.org/x/mobile
  go mod tidy
  mkdir -p "$awlflutterdir/android/app/src/main/libs"
  mv anywherelan.aar "$awlflutterdir/android/app/src/main/libs/"
}

# build for android, require mobile lib
build-mobile-apk() {
  cd "$awlflutterdir"
  flutter build apk --release
  mv "$awlflutterdir/build/app/outputs/flutter-apk/app-release.apk" "$builddir/awl-android-multiarch-$VERSION.apk"
}

# build for android
build-mobile() {
  build-mobile-lib
  build-mobile-apk
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
  gobuild-windows awl-tray
  build-awl-tray-linux-cross
}

# build desktop version based on current environment
build-awl-tray() {
  goos="$(go env GOOS)"
  arch="$(go env GOARCH)"
  filename="awl-tray-$goos-$arch-$VERSION"
  if [ "$goos" == "windows" ] ;then
    filename="$filename.exe"
  fi
  cd "$awldir/cmd/awl-tray"
  go build -trimpath -ldflags "-s -w -X github.com/anywherelan/awl/config.Version=${VERSION}" -o "$filename"
  # set host's rights because when build from docker it will be root:root
  host_uid="$(stat -c "%u" "$builddir")"
  host_gid="$(stat -c "%g" "$builddir")"
  chown "$host_uid:$host_gid" "$filename"
  mv "$filename" "$builddir"
}

build-awl-tray-linux-cross() {
  cd "$awldir"
  for arch in 386 amd64 arm arm64; do
    docker run --rm -v "$PWD":/usr/src/myapp -w /usr/src/myapp "awl-cross-$arch" /bin/sh -c './build.sh awl-tray'
  done
}

# build server and desktop versions
build-desktop-cross() {
  build-awl-cross
  build-awl-tray-cross
}

build-docker-images() {
  for arch in 386 amd64 arm arm64; do
    docker build -t "awl-cross-$arch" "./crosscompile/linux-$arch"
  done
}

case "${1:-default}" in
release)
  clean
  download-wintun
  build-web
  build-mobile
  build-desktop-cross
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
awl-tray)
  download-wintun
  build-awl-tray
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
