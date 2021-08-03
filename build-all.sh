#!/bin/bash

awldir=$(pwd)
builddir="$awldir/build"
awlflutterdir="$awldir/../awl-flutter"

version=$(git describe --tags)

rm -rf build/
mkdir build
rm -rf static/

gobuild() {
	name="$1"
	for arch in 386 amd64 arm arm64; do
	  filename="$name-linux-$arch-$version"
	  GOOS=linux GOARCH=$arch go build -o $filename
	  mv $filename $builddir
  done

	for arch in 386 amd64; do
	  filename="$name-windows-$arch-$version.exe"
	  GOOS=windows GOARCH=$arch go build -ldflags "-H windowsgui" -o $filename
	  mv $filename $builddir
  done
}


cd $awlflutterdir
flutter build web --release
cp -r "$awlflutterdir/build/web" "$awldir/static"

cd "$awldir/cmd/gomobile-lib"
gomobile bind -o anywherelan.aar -target=android .
mv anywherelan.aar "$awlflutterdir/android/app/src/main/libs/"

cd $awlflutterdir
flutter build apk --release
mv "$awlflutterdir/build/app/outputs/flutter-apk/app-release.apk" "$builddir/awl-android-multiarch-$version.apk"

cd "$awldir/cmd/awl"
gobuild awl

cd "$awldir/cmd/awl-tray"
gobuild awl-tray
