# NDJSON Sort

Stable external merge sort for newline-delimited JSON. Sort input larger than RAM without an in-memory index of either the records or the spill files. Feed it a file or a pipe; it writes the original record bytes in sorted order.

```sh
go build .
printf '%s\n' '{"key":"b","id":1}' '{"key":"a","id":2}' '{"key":"a","id":3}' |
  ./ndjson-sort -key key -stats
```

Output:

```json
{"key":"a","id":2}
{"key":"a","id":3}
{"key":"b","id":1}
```

## Algorithm and resource bounds

1. Read and validate one record at a time. Accumulate an approximately byte-budgeted run, sort it by the decoded key and original sequence, and spill binary-framed records to a private temporary directory.
2. Merge at most `fan-in` runs at once with a heap storing one head per run. Repeat with intermediate runs until final output; names are derived from pass/ordinal counters, so no all-runs list stays in memory.
3. Preserve original order for equal keys using the original global sequence as a tie-breaker in every merge pass. The literal JSON is not re-serialized. Blank lines are skipped, and LF is emitted after each record, including the last.

Input keys must be top-level string fields, compared lexicographically by UTF-8 bytes. Go JSON decoding uses the last value for duplicate fields. Actual key case and Unicode normalization are not altered. The maximum record length is enforced before parsing; additional parsing allocations are proportional to record size. Run memory scales with the run budget and a bounded number of records, and merge memory scales with fan-in times max record length. These are algorithmic bounds, not a hard Go-heap or RSS cap. Merge opens at most fan-in input files plus one intermediate output; each extra merge pass reads and writes all records once, apart from the final pass that writes user output.

All input is validated before output starts. Any later disk/output error can produce partial stdout; write to a caller-owned temporary file and rename it on success if you require atomic replacement. Spill files are removed on success, errors and cooperative cancellation. A blocked reader/writer may delay cancellation; forced termination or machine failure can leave temp files. Spill framing is internal and not a persistent or checksummed file format. Records are plaintext on spill storage: choose an appropriate `-tmpdir` for sensitive input.

## Verify

Go 1.23+ and no third-party dependencies. The benchmark also uses Python 3 on Unix for wall time and peak child RSS.

```sh
go test -race ./...
go vet ./...
./scripts/bench.sh 300000
```

The benchmark measures only the sorter process, not the generator or Python reference. The verifier compares every output record byte-for-byte to a stable in-memory reference and checks spill cleanup. See [BENCHMARK.md](BENCHMARK.md) for a measured run and its limits.

This portfolio prototype was implemented with AI assistance. It is not a production deployment or a claim of independent implementation.
