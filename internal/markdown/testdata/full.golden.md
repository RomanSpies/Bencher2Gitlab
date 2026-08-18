<!-- bencher2gitlab id="test" -->
## Bencher Report [gcc]: 2 active alerts ⚠️ (1 inactive)

**Project:** `BankingSync` | **Branch:** `feature/ebics-batch` | **Testbed:** `gcc` | [Full report](https://bencher.dev/perf/bankingsync/reports/7d3f9b2c-4a1e-4c8b-9f0d-2e5a6b7c8d9e)

| Testbed | Benchmark | Measure (units) | Value | Lower Boundary | Upper Boundary |
| --- | --- | --- | ---: | ---: | ---: |
| `gcc` | `BenchmarkParseEBICS (iter 0)` | Latency (nanoseconds (ns)) | 1234.56 (+25.31%) | 900 | **1080 (114.31%)** ⚠️ |
| `gcc` | `BenchmarkAllocScrub (iter 1)` | Throughput (operations / second (ops/s)) | 4200 (-16.00%) | **4600 (91.30%)** ⚠️ | — |
