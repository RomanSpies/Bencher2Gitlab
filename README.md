# bencher2gitlab

[Bencher](https://bencher.dev) can post benchmark results straight into GitHub
pull requests, but there's no GitLab equivalent — the docs stop at "call the
API yourself". This tool is that missing piece: it takes the JSON report from
`bencher run` and posts it as a comment on your merge request.

```
bencher run --quiet --format json "go test -bench=. ./..." | bencher2gitlab
```

There's no statistics magic in here. Bencher's API already decides server-side
whether a result is a regression; the report it hands back contains the
finished alerts. bencher2gitlab just turns that JSON into a Markdown table and
puts it where people actually look.

The comment ends up looking like this:

> ## Bencher Report [gcc]: 2 active alerts ⚠️
>
> **Project:** `BankingSync` | **Branch:** `feature/x` | **Testbed:** `gcc` | [Full report](…)
>
> | Testbed | Benchmark | Measure (units) | Value | Lower Boundary | Upper Boundary |
> | --- | --- | --- | ---: | ---: | ---: |
> | `gcc` | `BenchmarkParseEBICS` | Latency (ns) | 1234.56 (+25.31%) | 900 | **1080 (114.31%)** ⚠️ |

Pushing to the MR again updates the existing comment instead of adding a new
one. Each comment carries a hidden HTML marker
(`<!-- bencher2gitlab id="project/branch/testbed/adapter" -->`), so the tool
can find its own comment among everything else on the MR.

## Post first, fail second

Run `bencher run` **without** `--err`. If it fails on a regression, the report
never makes it to the MR. Instead, let bencher2gitlab do the failing: it posts
the comment and *then* exits with code 1 when there are active alerts. Your
job goes red, and the reason is right there in the MR.

Exit codes: `0` no active alerts, `1` active alerts (comment was posted
first), `2` something went wrong (bad input, API error — this wins over 1).

## You need a real token

This is the annoying part, and it's a GitLab limitation, not ours:
`CI_JOB_TOKEN` can *read* MR comments but gets a 403 when creating or
updating one. Even the fine-grained job token permissions (GA since GitLab
18.3) only cover reading notes, not writing them — the feature request is
[gitlab-org/gitlab#464591](https://gitlab.com/gitlab-org/gitlab/-/issues/464591).

So: create a project access token with the `api` scope and put it in a masked
CI/CD variable called `BENCHER2GITLAB_TOKEN` (or `GITLAB_TOKEN`). The token is
only ever read from the environment, never from a flag, so it doesn't leak
into `ps` output or shell history.

## CI setup

```yaml
benchmark:
  stage: test
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  script:
    - bencher run --project my-project
        --branch "$CI_MERGE_REQUEST_SOURCE_BRANCH_NAME"
        --testbed ci-runner --adapter go_bench
        --quiet --format json "go test -bench=. ./..." > report.json
    - bencher2gitlab --report report.json
```

`CI_API_V4_URL`, `CI_PROJECT_ID` and `CI_MERGE_REQUEST_IID` are picked up
automatically; only the token variable needs to be configured.

If you run benchmarks on several testbeds in the same MR — say a gcc job and
a clang job — each one gets its own comment, because the testbed is part of
the marker. The jobs don't need to know about each other. If the default
segmentation doesn't fit, `--ci-id` overrides it.

## Flags

```
--report PATH        JsonReport file, "-" for stdin (default "-")
--gitlab-url URL     GitLab API v4 base (default $CI_API_V4_URL)
--project ID|PATH    project (default $CI_PROJECT_ID)
--mr IID             merge request IID (default $CI_MERGE_REQUEST_IID)
--ci-id STRING       override the idempotency marker id
--bencher-url URL    Bencher console base for the report link (default https://bencher.dev)
--on-zero-alerts M   post (default) | auto (only update an existing comment) | skip
--timeout DUR        total HTTP timeout (default 30s)
--dry-run            print the markdown to stdout and do nothing else
```

`--dry-run` is handy for checking what a report renders to:

```
./bencher2gitlab --report internal/report/testdata/full.json --dry-run
```

## Development

Plain Go, no dependencies — three REST endpoints didn't seem worth a GitLab
SDK. The report structs were written against Bencher's Rust types
(`lib/bencher_json`) and their OpenAPI spec; parsing is deliberately lenient
so new upstream fields won't break anything.

Tests never talk to a real GitLab. `internal/gitlab/gitlabtest` fakes the
Notes API on `httptest`, including pagination and the job-token 403 behavior,
and the markdown output is covered by golden files:

```
go test ./...
go test ./internal/markdown -update   # regenerate golden files
```

No retries yet — if GitLab hiccups, rerun the job.
