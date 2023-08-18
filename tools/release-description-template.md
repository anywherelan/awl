# Installation

For instructions on how to install anywherelan [see readme](https://github.com/anywherelan/awl#installation).

## Android

[{{$.AwlAndroid}}](https://github.com/anywherelan/awl/releases/download/{{$.ReleaseTag}}/{{$.AwlAndroid}})

## Desktop version (awl-tray)

### Windows binary builds

{{range .AwlTrayWindows}}
[{{.}}](https://github.com/anywherelan/awl/releases/download/{{$.ReleaseTag}}/{{.}})  {{end}}

### macOS binary builds

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
