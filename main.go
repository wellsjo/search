package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/jessevdk/go-flags"

	"github.com/wellsjo/SuperSearch/search"
)

func main() {
	var (
		pattern  string
		location string
		opts     search.Options
	)

	parser := flags.NewParser(&opts, flags.Default)
	parser.Usage = "[OPTIONS] PATTERN [PATH]"
	args, err := parser.Parse()

	if err != nil {
		fail(err)
	}

	if len(args) == 0 {
		parser.WriteHelp(os.Stdout)
		return
	}

	if len(args) > 0 {
		pattern = args[0]
	}

	if len(args) > 1 {
		location = args[1]
	}

	if pattern == "" {
		parser.WriteHelp(os.Stdout)
		return
	}

	if location == "" {
		location = "." + string(filepath.Separator)
	}

	if opts.Debug {
		search.DebugMode = true
	}

	ss := search.New(&search.Options{
		Pattern:      pattern,
		Location:     location,
		Quiet:        opts.Quiet,
		Hidden:       opts.Hidden,
		Unrestricted: opts.Unrestricted,
		Debug:        opts.Debug,
	})

	err = ss.Run()
	if err != nil {
		fail(err)
	}
}

func fail(s ...interface{}) {
	log.Println(s...)
	os.Exit(1)
}
