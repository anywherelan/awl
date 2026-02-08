# Building Anywherelan

## Dependencies

* Go (1.25)
* Git
* gomobile and Android Studio for Android ([see more](https://pkg.go.dev/golang.org/x/mobile/cmd/gomobile))
* Flutter (3.38) for Web and Android

## Release build

The first step is to clone [awl](https://github.com/anywherelan/awl) and [awl-flutter](https://github.com/anywherelan/awl-flutter) in one parent directory.

```bash
# Example structure:
# /workspace
#   ├── awl
#   └── awl-flutter

git clone https://github.com/anywherelan/awl.git
git clone https://github.com/anywherelan/awl-flutter.git

cd awl
./build.sh release
ls build
```

See [build.sh](build.sh) for more details.

## Local Development

**Note:** To run the application on desktop (Linux/macOS/Windows), you need **root/administrator** rights. This is required to create virtual network interfaces.

### Web Static Files

The project requires a `static` directory containing the Flutter web UI. If you see an error like `pattern static: no matching files found`, you need to set this up. You have two options:

**Option A: Download pre-built (No Flutter required)**  
If you don't want to install Flutter or build the frontend, you can download the pre-built static files.
1. Download the `awl-release-static.zip` artifact from a recent GitHub Action run from [Manual build release workflow](https://github.com/anywherelan/awl/actions/workflows/build-manual.yml).
2. Unzip the contents into the `static` directory in the root of the awl project:
   ```bash
   unzip awl-release-static.zip -d static
   ```

**Option B: Build locally**  
If you have [Flutter installed](#dependencies), you can build the web static files yourself:
```bash
./build.sh web
```
This rebuilds the Flutter web frontend and places it in the `static` directory.

### For Windows

If you are developing on Windows, you must download the `wintun` driver dependencies before building:
```bash
./build.sh deps
```

### For Android

The `./build.sh` script provides commands for Android development:

- **Build Android Library (Go backend)**:
  ```bash
  ./build.sh android-lib
  ```
  Builds the Go backend as an Android library (`anywherelan.aar`). Use this when you are working on the Go code and want to check for compilation errors without building the full APK.

- **Build Full Android APK**:
  ```bash
  ./build.sh android
  ```
  Builds the full Android application (requires `android-lib` step implicitly, but this command runs both).




