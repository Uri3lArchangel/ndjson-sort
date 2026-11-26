package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

func opts(t testing.TB) Options {
	return Options{Key: "k", RunBytes: 384, MaxRecord: 128, FanIn: 2, TempDir: t.TempDir()}
}
func assertClean(t *testing.T, dir string) {
	t.Helper()
	x, e := os.ReadDir(dir)
	if e != nil || len(x) != 0 {
		t.Fatalf("spill directory left behind: %v %v", x, e)
	}
}
func TestMultipassStableAndPreservesRawRecords(t *testing.T) {
	var input, expected strings.Builder
	for i := 0; i < 97; i++ {
		fmt.Fprintf(&input, " { \"k\" : \"%d\" , \"i\": %d }\n", i%3, i)
	}
	for k := 0; k < 3; k++ {
		for i := k; i < 97; i += 3 {
			fmt.Fprintf(&expected, " { \"k\" : \"%d\" , \"i\": %d }\n", k, i)
		}
	}
	var got bytes.Buffer
	o := opts(t)
	s, e := Sort(context.Background(), strings.NewReader(strings.TrimSuffix(input.String(), "\n")), &got, o)
	if e != nil || got.String() != expected.String() || s.Records != 97 || s.Runs < 8 || s.MergePasses < 3 {
		t.Fatalf("invalid sort: %+v %v (output %q)", s, e, got.String())
	}
	assertClean(t, o.TempDir)
}
func TestRejectsMalformedBeforeWritingAndCleansUp(t *testing.T) {
	for _, line := range []string{`{"k":null}`, `{"k":2}`, `{"other":""}`, `[{"k":"ok"}]`, `{"k":`, strings.Repeat("x", 130), "{\"k\":\"\xff\"}"} {
		t.Run(fmt.Sprintf("%q", line), func(t *testing.T) {
			input := strings.Repeat("{\"k\":\"ok\"}\n", 32) + line
			var got bytes.Buffer
			o := opts(t)
			s, e := Sort(context.Background(), strings.NewReader(input), &got, o)
			if e == nil || !strings.Contains(e.Error(), "line 33:") || got.Len() != 0 || s.Runs == 0 {
				t.Fatalf("expected validated failure with prior spills: %+v %v %d", s, e, got.Len())
			}
			assertClean(t, o.TempDir)
		})
	}
}

type callbackReader struct {
	onRead func()
	r      io.Reader
}

func (r callbackReader) Read(p []byte) (int, error) { r.onRead(); return r.r.Read(p) }

type callbackWriter func([]byte) (int, error)

func (w callbackWriter) Write(p []byte) (int, error) { return w(p) }
func TestCancelAfterSpill(t *testing.T) {
	o := opts(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := 0
	input := callbackReader{func() {
		n++
		if n == 2 {
			cancel()
		}
	}, strings.NewReader(strings.Repeat("{\"k\":\"ok\"}\n", 1000))}
	s, e := Sort(ctx, input, io.Discard, o)
	if !errors.Is(e, context.Canceled) || s.Runs == 0 {
		t.Fatalf("expected cancellation after spill: %+v %v", s, e)
	}
	assertClean(t, o.TempDir)
}
func TestOutputErrorCleansSpills(t *testing.T) {
	o := opts(t)
	fail := errors.New("disk full")
	output := callbackWriter(func([]byte) (int, error) { return 0, fail })
	_, e := Sort(context.Background(), strings.NewReader(strings.Repeat("{\"k\":\"ok\"}\n", 400)), output, o)
	if !errors.Is(e, fail) {
		t.Fatalf("expected wrapped write error: %v", e)
	}
	assertClean(t, o.TempDir)
}
func TestCLI(t *testing.T) {
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"-key", "n", "-stats", "-tmpdir", t.TempDir()}, strings.NewReader("{\"n\":\"z\"}\n\n{\"n\":\"a\"}"), &out, &stderr)
	if code != 0 || out.String() != "{\"n\":\"a\"}\n{\"n\":\"z\"}\n" || !strings.Contains(stderr.String(), `"Records":2`) {
		t.Fatalf("exit %d stdout %q stderr %q", code, out.String(), stderr.String())
	}
}

func TestLargeRecordCrossesReaderBuffer(t *testing.T) {
	o := Options{Key: "k", RunBytes: 33000, MaxRecord: 16384, FanIn: 2, TempDir: t.TempDir()}
	head := "{\"k\":\"é\",\"payload\":\"" + strings.Repeat("x", 8192) + "\"}"
	tail := "{\"k\":\"b\"}"
	var out bytes.Buffer
	_, err := Sort(context.Background(), strings.NewReader(head+"\n"+tail), &out, o)
	if err != nil || out.String() != tail+"\n"+head+"\n" {
		t.Fatalf("large record not read intact: output length %d, error %v", out.Len(), err)
	}
	assertClean(t, o.TempDir)
}
