#!/bin/bash

awldir=$(pwd)
builddir="$awldir/build"
awlflutterdir="$awldir/../awl-flutter"

version=$(git describe --tags)

rm -rf build/
mkdir build
rm -rf static/

wintun_version="wintun-0.11"
if [[ ! -a "/tmp/$wintun_version" ]]; then
  wget "https://www.wintun.net/builds/$wintun_version.zip"
  unzip "$wintun_version.zip" -d "/tmp/$wintun_version"
  rm -f "$wintun_version.zip"
fi

gobuild() {
	name="$1"
	for arch in 386 amd64 arm arm64; do
	  filename="$name-linux-$arch-$version"
	  GOOS=linux GOARCH=$arch go build -o "$filename"
	  mv "$filename" "$builddir"
  done

  for tuple in "386 x86" "amd64 amd64"
  do
    goarch=$(echo "$tuple" | cut -f1 -d" ")
    wintunarch=$(echo "$tuple" | cut -f2 -d" ")
    cp "/tmp/$wintun_version/wintun/bin/$wintunarch/wintun.dll" wintun.dll

	  filename="$name-windows-$goarch-$version.exe"
	  GOOS=windows GOARCH=$goarch go build -ldflags "-H windowsgui" -o "$filename"
	  mv "$filename" "$builddir"
    rm -f "wintun.dll"
  done
}


cd "$awlflutterdir"
flutter build web --release
cp -r "$awlflutterdir/build/web" "$awldir/static"

cd "$awldir/cmd/gomobile-lib"
gomobile bind -o anywherelan.aar -target=android .
mv anywherelan.aar "$awlflutterdir/android/app/src/main/libs/"

cd "$awlflutterdir"
flutter build apk --release
mv "$awlflutterdir/build/app/outputs/flutter-apk/app-release.apk" "$builddir/awl-android-multiarch-$version.apk"

cd "$awldir/cmd/awl"
gobuild awl

cd "$awldir/cmd/awl-tray"
gobuild awl-tray
