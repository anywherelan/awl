# Building Anywherelan

## Dependencies

* Go (1.24)
* Git
* gomobile and Android Studio for Android ([see more](https://pkg.go.dev/golang.org/x/mobile/cmd/gomobile))
* Flutter (3.27)

## Build

The first step is to clone [awl](https://github.com/anywherelan/awl) and [awl-flutter](https://github.com/anywherelan/awl-flutter) in one parent directory.

```bash
cd awl
./build.sh release
ls build
```

See [build.sh](build.sh) for more details.
