# Building Anywherelan

## Dependencies
  * Go (1.16)
  * Git
  * gcc, gtk3, libappindicator3 for awl-tray on Linux ([see more](https://github.com/anywherelan/systray#platform-notes))
  * gomobile and Android Studio for Android ([see more](https://pkg.go.dev/golang.org/x/mobile/cmd/gomobile))
  * Flutter (2.2)

## Build

The first step is to clone [awl](https://github.com/anywherelan/awl) and [awl-flutter](https://github.com/anywherelan/awl-flutter) in one parent directory.

```bash
cd awl
./build-all.sh
ls build
```

See [build-all.sh](build-all.sh) for more details.
