# Reproducible measurement

Measured in a Linux x86-64 sandbox with Go 1.25.3. Both runs use the same synthetic workload (fixed PRNG seed, 2,048 possible string keys and a 180-byte data field), 2 MiB run budget, a 64 KiB record limit and merge fan-in of four.

| Rows | Input bytes | Initial runs | Merge passes | Wall time | Peak RSS |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 300,000 | 64,688,890 | 50 | 3 | 1.22 s | 9,460 KiB |
| 1,200,000 | 259,288,890 | 199 | 4 | 6.85 s | 9,816 KiB |

Re-run with `bash scripts/bench.sh 300000` or `bash scripts/bench.sh 1200000`. The script validates the entire sorted output against Python's stable in-memory sort, comparing original record bytes, and checks that the spill parent is empty afterward. This validator holds the full input in memory; it is intentionally separate from the measured sorter subprocess.

These are one-off measurements, not a performance SLA or comparison with another sorter. Disk caching, memory allocator behavior, CPU scheduling and record/key sizes affect the results. A fourfold input size increased run count and added a merge pass; observed sorter RSS remained below 10 MiB here. The run byte budget controls buffered input, not total process RSS.
