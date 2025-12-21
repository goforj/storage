package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	var file string
	var module string
	var enable bool
	var disable bool
	flag.StringVar(&file, "file", "", "path to go.mod to edit")
	flag.StringVar(&module, "module", "github.com/goforj/filesystem-rclone", "module path to toggle replace for")
	flag.BoolVar(&enable, "enable", false, "uncomment the filesystem-rclone replace")
	flag.BoolVar(&disable, "disable", false, "comment the filesystem-rclone replace")
	flag.Parse()

	if file == "" {
		exitErr("missing -file")
	}
	if module == "" {
		exitErr("missing -module")
	}
	if enable == disable {
		exitErr("set exactly one of -enable or -disable")
	}

	data, err := os.ReadFile(file)
	if err != nil {
		exitErr("read file: %v", err)
	}

	target := "replace " + module
	commented := "// " + target
	contents := string(data)

	switch {
	case enable:
		if strings.Contains(contents, commented) {
			contents = strings.Replace(contents, commented, target, 1)
		}
	case disable:
		if strings.Contains(contents, target) {
			contents = strings.Replace(contents, target, commented, 1)
		}
	}

	if err := os.WriteFile(file, []byte(contents), 0o644); err != nil {
		exitErr("write file: %v", err)
	}
}

func exitErr(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
