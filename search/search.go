package search

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/fatih/color"
	"golang.org/x/exp/mmap"
)

var (
	ignoreFilePatterns   = []string{}
	globalIgnoreFiles    = [...]string{".gitignore_global"}
	ignoreFiles          = [...]string{".gitignore"}
	globalIgnorePatterns = []*regexp.Regexp{}

	// Set concurrency to # cores
	concurrency = runtime.NumCPU()

	highlightMatch  = color.New(color.BgYellow).Add(color.FgBlack).Add(color.Bold)
	highlightFile   = color.New(color.FgCyan).Add(color.Bold)
	highlightNumber = color.New(color.FgGreen).Add(color.Bold)
)

type Options struct {
	Pattern  string
	Location string
	Debug    bool
}

type SuperSearch struct {
	opts *Options

	searchRegexp *regexp.Regexp
	searchPaths  chan *string

	// Global wait group
	wg *sync.WaitGroup
}

func New(opts *Options) *SuperSearch {
	Debug("Searching %v for %v", opts.Location, opts.Pattern)
	Debug("Concurrency", concurrency)
	return &SuperSearch{
		opts:         opts,
		searchRegexp: regexp.MustCompile(opts.Pattern),

		// Allow enough files in the buffer so that there will always be plenty
		// for the worker threads
		searchPaths: make(chan *string, 1024),

		wg: new(sync.WaitGroup),
	}
}

func (ss *SuperSearch) Run() {
	ss.wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go ss.worker()
	}
	ss.findFiles()
	close(ss.searchPaths)
	ss.wg.Wait()
}

func (ss *SuperSearch) findFiles() {
	fi, err := os.Stat(ss.opts.Location)
	if err != nil {
		Fail(err)
	}
	switch mode := fi.Mode(); {
	case mode.IsDir():
		ss.scanDir(&ss.opts.Location)
	case mode.IsRegular():
		ss.searchPaths <- &ss.opts.Location
	}
}

func (ss *SuperSearch) scanDir(dir *string) {
	Debug("Scanning directory %v", dir)
	dirInfo, err := ioutil.ReadDir(*dir)
	if err != nil {
		Fail("io error: failed to read directory. %v", err)
	}
	for _, fi := range dirInfo {
		if fi.Name()[0] == '.' {
			continue
		}
		path := filepath.Join(*dir, fi.Name())
		if fi.IsDir() {
			ss.scanDir(&path)
		} else if fi.Mode().IsRegular() {
			ss.searchPaths <- &path
			Debug("Queuing %v", path)
		}
	}
	Debug("Scan dir finished %v", dir)
}

func (ss *SuperSearch) worker() {
	Debug("Started worker")
	var output *strings.Builder
	for path := range ss.searchPaths {
		ss.searchFile(path, output)
	}
	if output.Len() > 0 {
		fmt.Print(output.String())
	}
	ss.wg.Done()
}

func (ss *SuperSearch) searchFile(path *string, output *strings.Builder) {
	file, err := mmap.Open(*path)
	if err != nil {
		Fail("Failed to open file with mmap", path)
	}
	defer file.Close()

	if isBin(file) || file.Len() == 0 {
		return
	}

	lastIndex := 0
	lineNo := 1
	buf := make([]byte, file.Len())
	bytesRead, err := file.ReadAt(buf, 0)
	if err != nil {
		Fail("Failed to read file", *path+".", "Read", bytesRead, "bytes.")
	}

	for i := 0; i < len(buf); i++ {
		if buf[i] == '\n' {
			var line = buf[lastIndex:i]
			ixs := ss.searchRegexp.FindAllIndex(line, -1)

			if ixs != nil {
				output.Write([]byte(highlightNumber.Sprint(lineNo, ":")))
				lastIndex := 0

				for _, i := range ixs {
					output.Write([]byte(fmt.Sprint(string(line[lastIndex:i[0]]))))
					output.Write([]byte(highlightMatch.Sprint(string(line[i[0]:i[1]]))))
					lastIndex = i[1]
				}
				output.Write([]byte(fmt.Sprintln(string(line[lastIndex:]))))
			}

			lastIndex = i + 1
			lineNo++
		}
	}
}

// Cheap (at the expense of being janky) way to determine if a file is binary
func isBin(file *mmap.ReaderAt) bool {
	var offsetLen int64 = int64(file.Len()) / 4
	var offset int64 = 0
	var buf = make([]byte, 4)
	for i := 0; i < 4; i++ {
		file.ReadAt(buf, offset)
		if !utf8.Valid(buf) {
			return true
		}
		offset += offsetLen
	}
	return false
}
