package main

import (
	"bufio"
	"bytes"
	"container/heap"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"unicode/utf8"
)

type Options struct {
	Key                        string
	RunBytes, MaxRecord, FanIn int
	TempDir                    string
}
type Stats struct {
	Records, Runs, MergePasses uint64
}
type record struct {
	key  string
	line []byte
	seq  uint64
}

func less(a, b record) bool {
	if a.key == b.key {
		return a.seq < b.seq
	}
	return a.key < b.key
}

// Sort requires one JSON object per non-empty line and a top-level string key.
// It preserves the input order of equal keys, even across merge passes.
func Sort(ctx context.Context, input io.Reader, output io.Writer, opts Options) (stats Stats, err error) {
	const maxFanIn = 128
	if opts.Key == "" || opts.MaxRecord < 1 || opts.MaxRecord > 64<<20 || opts.RunBytes < 2*opts.MaxRecord+128 || opts.FanIn < 2 || opts.FanIn > maxFanIn {
		return stats, errors.New("require key, max-record in [1,64 MiB], run-bytes >= 2*max-record+128, and fan-in in [2,128]")
	}
	if err = ctx.Err(); err != nil {
		return stats, err
	}
	dir, err := os.MkdirTemp(opts.TempDir, "ndjson-sort-")
	if err != nil {
		return stats, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	path := func(pass, num uint64) string {
		return filepath.Join(dir, fmt.Sprintf("%d-%d.run", pass, num))
	}
	in := bufio.NewReader(input)
	var batch []record
	nbytes := 0
	spill := func() error {
		sort.Slice(batch, func(i, j int) bool { return less(batch[i], batch[j]) })
		if err := ctx.Err(); err != nil {
			return err
		}
		f, e := os.Create(path(0, stats.Runs))
		if e != nil {
			return e
		}
		w := bufio.NewWriter(f)
		for _, r := range batch {
			if e = ctx.Err(); e != nil {
				break
			}
			if e = writeRun(w, r); e != nil {
				break
			}
		}
		if e == nil {
			e = w.Flush()
		}
		e = errors.Join(e, f.Close())
		if e == nil {
			stats.Runs++
		}
		batch = nil
		nbytes = 0
		return e
	}
	var lineNumber uint64
	for {
		if err = ctx.Err(); err != nil {
			return stats, err
		}
		raw, e := readLine(in, opts.MaxRecord)
		if e == io.EOF {
			break
		}
		lineNumber++
		if e != nil {
			return stats, fmt.Errorf("line %d: %w", lineNumber, e)
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		// A type assertion on a RawMessage map would accept null into a Go
		// string. Check the encoded type before decoding the sort key.
		var obj map[string]json.RawMessage
		if !utf8.Valid(raw) {
			return stats, fmt.Errorf("line %d: invalid UTF-8", lineNumber)
		}
		if e = json.Unmarshal(raw, &obj); e != nil || obj == nil {
			return stats, fmt.Errorf("line %d: expected a JSON object", lineNumber)
		}
		value := obj[opts.Key]
		var key string
		if len(value) == 0 || value[0] != '"' || json.Unmarshal(value, &key) != nil {
			return stats, fmt.Errorf("line %d: field %q must be a string", lineNumber, opts.Key)
		}
		cost := len(raw) + len(key) + 128
		if cost > opts.RunBytes-nbytes {
			if err = spill(); err != nil {
				return stats, err
			}
		}
		if stats.Records == ^uint64(0) {
			return stats, errors.New("record counter overflow")
		}
		batch = append(batch, record{key, raw, stats.Records})
		stats.Records++
		nbytes += cost
	}
	if len(batch) > 0 {
		if err = spill(); err != nil {
			return stats, err
		}
	}
	count := stats.Runs
	final := bufio.NewWriter(output)
	for count > 0 {
		inPass := stats.MergePasses
		var next uint64
		for base := uint64(0); base < count; {
			size := min(uint64(opts.FanIn), count-base)
			files := make([]string, 0, int(size))
			for i := base; i < base+size; i++ {
				files = append(files, path(inPass, i))
			}
			if count <= uint64(opts.FanIn) {
				err = merge(ctx, files, opts.MaxRecord, func(r record) error {
					if _, e := final.Write(r.line); e != nil {
						return e
					}
					return final.WriteByte('\n')
				})
			} else {
				err = mergeToRun(ctx, files, opts.MaxRecord, path(inPass+1, next))
			}
			if err != nil {
				return stats, err
			}
			for _, f := range files {
				if err = os.Remove(f); err != nil {
					return stats, err
				}
			}
			next++
			base += size
		}
		stats.MergePasses++
		if count <= uint64(opts.FanIn) {
			break
		}
		count = next
	}
	if err = ctx.Err(); err != nil {
		return stats, err
	}
	return stats, final.Flush()
}

func readLine(in *bufio.Reader, maxRecord int) ([]byte, error) {
	var line []byte
	for {
		part, e := in.ReadSlice('\n')
		part = bytes.TrimSuffix(part, []byte{'\n'})
		if len(line)+len(part) > maxRecord {
			return nil, errors.New("record exceeds max-record")
		}
		line = append(line, part...)
		if e == bufio.ErrBufferFull {
			continue
		}
		if e == io.EOF && len(line) > 0 {
			e = nil
		}
		return line, e
	}
}

// Run frame: little-endian sequence (u64), key size (u32), line size (u32),
// followed by the decoded UTF-8 key and original JSON bytes.
func writeRun(w *bufio.Writer, r record) error {
	var hdr [16]byte
	binary.LittleEndian.PutUint64(hdr[:8], r.seq)
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(len(r.key)))
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(r.line)))
	if _, e := w.Write(hdr[:]); e != nil {
		return e
	}
	if _, e := w.WriteString(r.key); e != nil {
		return e
	}
	_, e := w.Write(r.line)
	return e
}
func readRun(r *bufio.Reader, limit int) (record, error) {
	var hdr [16]byte
	if _, e := io.ReadFull(r, hdr[:]); e != nil {
		return record{}, e
	}
	k, n := binary.LittleEndian.Uint32(hdr[8:12]), binary.LittleEndian.Uint32(hdr[12:])
	if k > uint32(limit) || n > uint32(limit) || n == 0 {
		return record{}, errors.New("invalid spill frame lengths")
	}
	key := make([]byte, k)
	line := make([]byte, n)
	if _, e := io.ReadFull(r, key); e != nil {
		return record{}, fmt.Errorf("read frame key: %w", e)
	}
	if _, e := io.ReadFull(r, line); e != nil {
		return record{}, fmt.Errorf("read frame payload: %w", e)
	}
	return record{string(key), line, binary.LittleEndian.Uint64(hdr[:8])}, nil
}

type item struct {
	r     record
	index int
}
type heads []item

func (h heads) Len() int           { return len(h) }
func (h heads) Less(i, j int) bool { return less(h[i].r, h[j].r) }
func (h heads) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *heads) Push(x any)        { *h = append(*h, x.(item)) }
func (h *heads) Pop() any {
	old := *h
	i := len(old) - 1
	x := old[i]
	old[i] = item{}
	*h = old[:i]
	return x
}

func merge(ctx context.Context, paths []string, maxRecord int, emit func(record) error) (err error) {
	var h heads
	readers := make([]*bufio.Reader, len(paths))
	files := make([]*os.File, 0, len(paths))
	defer func() {
		for _, f := range files {
			err = errors.Join(err, f.Close())
		}
	}()
	for i, p := range paths {
		if err = ctx.Err(); err != nil {
			return err
		}
		f, e := os.Open(p)
		if e != nil {
			return e
		}
		files = append(files, f)
		readers[i] = bufio.NewReader(f)
		x, e := readRun(readers[i], maxRecord)
		if e == io.EOF {
			continue
		}
		if e != nil {
			return e
		}
		heap.Push(&h, item{x, i})
	}
	for len(h) > 0 {
		if err = ctx.Err(); err != nil {
			return err
		}
		x := heap.Pop(&h).(item)
		if err = emit(x.r); err != nil {
			return err
		}
		next, e := readRun(readers[x.index], maxRecord)
		if e == io.EOF {
			continue
		}
		if e != nil {
			return e
		}
		heap.Push(&h, item{next, x.index})
	}
	return nil
}
func mergeToRun(ctx context.Context, paths []string, maxRecord int, out string) error {
	f, e := os.Create(out)
	if e != nil {
		return e
	}
	w := bufio.NewWriter(f)
	e = merge(ctx, paths, maxRecord, func(r record) error { return writeRun(w, r) })
	if e == nil {
		e = w.Flush()
	}
	return errors.Join(e, f.Close())
}
