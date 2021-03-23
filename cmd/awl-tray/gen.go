// +build ignore

package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path"
	"path/filepath"
	"runtime"
)

func bundleFile(name string, path string, f *os.File) {
	res, err := ioutil.ReadFile(filepath.Clean(path))

	if err != nil {
		log.Println("Unable to load file "+path, err)
		return
	}

	_, err = f.WriteString(fmt.Sprintf("var %s = %#v\n", name, res))
	if err != nil {
		log.Println("Unable to write to bundled file", err)
	}
}

func openFile(filename string) *os.File {
	os.Remove(filename)
	_, dirname, _, _ := runtime.Caller(0)
	f, err := os.Create(path.Join(path.Dir(dirname), filename))
	if err != nil {
		log.Println("Unable to open file "+filename, err)
		return nil
	}

	_, err = f.WriteString("// **** THIS FILE IS AUTO-GENERATED, PLEASE DO NOT EDIT IT **** //\n\npackage main\n\n")
	if err != nil {
		log.Println("Unable to write file "+filename, err)
		return nil
	}

	return f
}

func main() {
	f := openFile("bundled.go")

	bundleFile("appIcon", "Icon.png", f)

	f.Close()
}
