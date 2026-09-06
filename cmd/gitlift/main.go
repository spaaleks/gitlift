package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/spaaleks/gitlift"
	"github.com/spaaleks/gitlift/internal/config"
	"github.com/spaaleks/gitlift/internal/logx"
	"github.com/spaaleks/gitlift/internal/tmpl"
	"github.com/spaaleks/gitlift/internal/tui"
)

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	showPaths := flag.Bool("paths", false, "print the config, template and log paths and exit")
	initConfig := flag.Bool("init", false, "write a starter config and exit")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "gitlift applies repository templates to GitHub and GitLab\n\n")
		fmt.Fprintf(os.Stderr, "usage: gitlift [flags]\n\nRun without flags to start the interface.\n\nflags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	switch {
	case *showVersion:
		fmt.Println("gitlift", gitlift.Version)
		return
	case *showPaths:
		printPaths()
		return
	case *initConfig:
		path := config.DefaultPath()
		if err := config.WriteExample(path); err != nil {
			fmt.Fprintln(os.Stderr, "gitlift:", err)
			os.Exit(1)
		}
		fmt.Println("wrote", path)
		return
	}

	if err := logx.Open(logx.DefaultPath()); err != nil {
		fmt.Fprintln(os.Stderr, "gitlift: cannot open log file:", err)
	}
	defer logx.Close()

	if err := tui.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "gitlift:", err)
		os.Exit(1)
	}
}

func printPaths() {
	fmt.Println("config search order:")
	for _, path := range config.SearchPaths() {
		marker := " "
		if _, err := os.Stat(path); err == nil {
			marker = "*"
		}
		fmt.Printf("  %s %s\n", marker, path)
	}

	saveDefault := ""
	if write := tmpl.WriteDirs(); len(write) > 0 {
		saveDefault = write[0]
	}

	fmt.Println("\ntemplate directories (searched in this order, and offered when saving a new one):")
	for _, dir := range tmpl.SearchDirs() {
		marker := " "
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			marker = "*"
		}
		note := ""
		if dir == saveDefault {
			note = "   (default when saving)"
		}
		fmt.Printf("  %s %s%s\n", marker, dir, note)
	}

	fmt.Println("\nlog file:")
	fmt.Printf("    %s\n", logx.DefaultPath())
}
