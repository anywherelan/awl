# Installation

For instructions on how to install anywherelan [see readme](https://github.com/anywherelan/awl#installation).

## Android

[{{$.AwlAndroid}}](https://github.com/anywherelan/awl/releases/download/{{$.ReleaseTag}}/{{$.AwlAndroid}})

## Desktop version (awl-tray)

### Windows binary builds

{{range .AwlTrayWindows}}
[{{.}}](https://github.com/anywherelan/awl/releases/download/{{$.ReleaseTag}}/{{.}})  {{end}}

### Windows 7 binary builds

{{range .AwlTrayWindows7}}
[{{.}}](https://github.com/anywherelan/awl/releases/download/{{$.ReleaseTag}}/{{.}})  {{end}}

### macOS app

(arm64 — Apple Silicon, amd64 — Intel)

{{range .AwlTrayMacos}}
[{{.}}](https://github.com/anywherelan/awl/releases/download/{{$.ReleaseTag}}/{{.}})  {{end}}

### Linux binary builds

{{range .AwlTrayLinux}}
[{{.}}](https://github.com/anywherelan/awl/releases/download/{{$.ReleaseTag}}/{{.}})  {{end}}

## Server version (awl)

### Linux binary builds

{{range .AwlLinux}}
[{{.}}](https://github.com/anywherelan/awl/releases/download/{{$.ReleaseTag}}/{{.}})  {{end}}

### Windows binary builds

{{range .AwlWindows}}
[{{.}}](https://github.com/anywherelan/awl/releases/download/{{$.ReleaseTag}}/{{.}})  {{end}}

### Windows 7 binary builds

{{range .AwlWindows7}}
[{{.}}](https://github.com/anywherelan/awl/releases/download/{{$.ReleaseTag}}/{{.}})  {{end}}

## Checksums (SHA-256)

Verify a downloaded file with `sha256sum -c` (or `shasum -a 256 -c`). Anywherelan's builds are reproducible — see [Reproducible builds](https://github.com/anywherelan/awl#reproducible-builds).

```
{{range .Checksums}}{{.Hash}}  {{.Name}}
{{end}}```
