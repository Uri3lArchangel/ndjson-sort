package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func run(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) int {
	var opts Options
	f := flag.NewFlagSet("ndjson-sort", flag.ContinueOnError)
	f.SetOutput(stderr)
	f.StringVar(&opts.Key, "key", "key", "top-level JSON string field")
	f.IntVar(&opts.RunBytes, "run-bytes", 32<<20, "estimated bytes of retained run records")
	f.IntVar(&opts.MaxRecord, "max-record", 1<<20, "maximum input line bytes, excluding LF")
	f.IntVar(&opts.FanIn, "fan-in", 16, "maximum input runs per merge")
	f.StringVar(&opts.TempDir, "tmpdir", "", "base directory for temporary runs")
	statsFlag := f.Bool("stats", false, "write build and merge counts to stderr as JSON")
	if e := f.Parse(args); e == flag.ErrHelp {
		return 0
	} else if e != nil {
		return 2
	}
	if f.NArg() != 0 {
		fmt.Fprintln(stderr, "ndjson-sort reads stdin and writes stdout; no positional arguments")
		return 2
	}
	stats, e := Sort(ctx, in, out, opts)
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if *statsFlag {
		if e = json.NewEncoder(stderr).Encode(stats); e != nil {
			return 1
		}
	}
	return 0
}
func main() {
	ctx, done := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	done()
	os.Exit(code)
}
