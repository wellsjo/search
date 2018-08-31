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
	"sync/atomic"
	"unicode/utf8"

	"github.com/fatih/color"
	"golang.org/x/exp/mmap"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var (
	ignoreFilePatterns   = []string{}
	globalIgnoreFiles    = [...]string{".gitignore_global"}
	ignoreFiles          = [...]string{".gitignore"}
	globalIgnorePatterns = []*regexp.Regexp{}

	// Setting max concurrency to # cpu cores gives best results
	maxConcurrency = runtime.NumCPU()

	highlightMatch  = color.New(color.BgYellow).Add(color.FgBlack).Add(color.Bold)
	highlightFile   = color.New(color.FgCyan).Add(color.Bold)
	highlightNumber = color.New(color.FgGreen).Add(color.Bold)
	highlightError  = color.New(color.FgRed).Add(color.Bold)
)

type Options struct {
	Usage    string
	Pattern  string
	Location string

	Quiet        bool `short:"q" long:"quiet" description:"Doesn't log any matches, just the results summary"`
	Hidden       bool `long:"hidden" description:"Search hidden files"`
	Unrestricted bool `short:"U" long:"unrestricted" description:"Search all files (ignore .gitignore)"`
	Debug        bool `short:"D" long:"debug" description:"Show verbose debug information"`
}

type searchFile struct {
	// Index represents the place the file has in the search queue. This is used
	// to reproduce the same output every time. All these numeric values are
	// uint64 so we don't have to convert in atomic.Add().
	index uint64
	path  string
}

type SuperSearch struct {
	opts *Options

	searchRegexp  *regexp.Regexp
	searchQueue   chan *searchFile
	numMatches    uint64
	filesMatched  uint64
	filesSearched uint64

	wg         *sync.WaitGroup
	numWorkers uint64
}

func New(opts *Options) *SuperSearch {
	debug("Searching %q for %q", opts.Location, opts.Pattern)
	return &SuperSearch{
		searchRegexp: regexp.MustCompile(opts.Pattern),
		opts:         opts,

		// Allow enough files in the buffer so that there will always be plenty
		// for the worker threads. This is an arbitrary large number.
		searchQueue: make(chan *searchFile),
		wg:          new(sync.WaitGroup),
	}
}

func (ss *SuperSearch) Run() {
	go ss.processFiles()
	ss.findFiles()
	close(ss.searchQueue)
	ss.wg.Wait()
	if !ss.opts.Quiet {
		ss.printResults()
	}
}

func (ss *SuperSearch) processFiles() {

}

func (ss *SuperSearch) findFiles() {
	fi, err := os.Stat(ss.opts.Location)
	if err != nil {
		fail("invalid location input")
	}
	switch mode := fi.Mode(); {
	case mode.IsDir():
		ss.scanDir(ss.opts.Location)
	case mode.IsRegular():
		ss.searchQueue <- &searchFile{
			path:  ss.opts.Location,
			index: 1,
		}
	}
}

// Recursively go through directory, sending all files into searchQueue
func (ss *SuperSearch) scanDir(dir string) {
	debug("Scanning directory %v", dir)
	ignore, _ := NewGitIgnoreFromFile(filepath.Join(dir, ".gitignore"))
	dirInfo, err := ioutil.ReadDir(dir)
	if err != nil {
		return
	}
	for _, fi := range dirInfo {
		if fi.Name()[0] == '.' {
			debug("Skipping hidden file %v", fi.Name())
			continue
		}
		if ignore.Match(fi.Name()) {
			debug("skipping gitignore match %v", fi.Name())
			continue
		}
		path := filepath.Join(dir, fi.Name())
		if fi.IsDir() {
			ss.scanDir(path)
		} else if fi.Mode().IsRegular() {
			atomic.AddUint64(&ss.filesSearched, 1)
			debug("Queuing %v", path)
			ss.searchQueue <- &searchFile{
				path:  path,
				index: ss.filesSearched,
			}
		}
	}
	debug("Finished scanning directory %v", dir)
}

// These run in parallel, taking files off of the searchQueue channel until it
// is finished
func (ss *SuperSearch) newWorker() {
	atomic.AddUint64(&ss.numWorkers, 1)
	debug("Started worker %v", ss.numWorkers)
	ss.wg.Add(1)
	go func() {
		for sf := range ss.searchQueue {
			ss.searchFile(sf)
		}
		ss.wg.Done()
	}()
}

func (ss *SuperSearch) searchFile(sf *searchFile) {
	file, err := mmap.Open(sf.path)
	if err != nil {
		return
	}
	defer file.Close()

	if isBin(file) {
		debug("Skipping binary file")
		return
	}

	if file.Len() == 0 {
		debug("Skipping empty file")
		return
	}

	var output strings.Builder
	matchFound := false
	lastIndex := 0
	lineNo := 1
	buf := make([]byte, file.Len())
	_, err = file.ReadAt(buf, 0)
	if err != nil {
		return
	}

	for i := 0; i < len(buf); i++ {
		if buf[i] == '\n' {
			var line = buf[lastIndex:i]
			ixs := ss.searchRegexp.FindAllIndex(line, -1)

			if ixs != nil {
				if !matchFound {
					matchFound = true
					atomic.AddUint64(&ss.filesMatched, 1)
					output.WriteString(highlightFile.Sprintf("%v\n", sf.path))
				}

				atomic.AddUint64(&ss.numMatches, 1)

				// Print line number, followed by each match
				output.WriteString(highlightNumber.Sprintf("%v:", lineNo))
				lastIndex := 0

				// Loop through match indexes, output highlighted match
				for _, i := range ixs {
					output.Write(line[lastIndex:i[0]])
					output.WriteString(highlightMatch.Sprint(string(line[i[0]:i[1]])))
					lastIndex = i[1]
				}
				output.Write(line[lastIndex:])
				output.WriteRune('\n')
			}

			lastIndex = i + 1
			lineNo++
		}
	}

	if matchFound {
		output.WriteRune('\n')
	}

	if !ss.opts.Quiet && output.Len() > 0 {
		fmt.Print(output.String())
	}
}

// Determine if file is binary by checking if it is valid utf8
func isBin(file *mmap.ReaderAt) bool {
	var (
		offsetStart = file.Len() / 3
		offsetEnd   = file.Len() / 2
	)
	var buf = make([]byte, offsetEnd-offsetStart)
	file.ReadAt(buf, int64(offsetStart))
	if !utf8.Valid(buf) {
		return true
	}
	return false
}

func (ss *SuperSearch) printResults() {
	var (
		p             = message.NewPrinter(language.English)
		matchesPlural = "s"
		filesPlural   = "s"
	)
	if ss.numMatches == 1 {
		matchesPlural = ""
	}
	if ss.filesMatched == 1 {
		filesPlural = ""
	}
	p.Printf("%v matche%s found in %v file%s (%v searched)",
		ss.numMatches, matchesPlural, ss.filesMatched,
		filesPlural, ss.filesSearched)
}
