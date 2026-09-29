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
can find its own comment among everything else on the MR. It only ever
touches comments written by the token's own user, so a reviewer quoting the
marker doesn't send it off trying to edit someone else's comment.

Huge reports get cut off at 64 KiB with a link to the full report on
Bencher. Nobody scrolls through a thousand rows in an MR comment anyway.

## Post first, fail second

Run `bencher run` **without** `--err`. If it fails on a regression, the report
never makes it to the MR. Instead, let bencher2gitlab do the failing: it posts
the comment and *then* exits with code 1 when there are active alerts. Your
job goes red, and the reason is right there in the MR.

Exit codes: `0` no active alerts, `1` active alerts (comment was posted
first), `2` something went wrong (bad input, API error — this wins over 1).

## Threads you can sign off

With `--thread` the report goes into a resolvable thread instead of a plain
comment. That matters if your project has "all threads must be resolved"
turned on: a regression now blocks the merge until somebody looks at it, and
a reviewer who decides the slowdown is fine can say so by resolving the
thread. That decision sticks. The thread remembers which regressions were
on the table when it was resolved (as a hidden list of fingerprints of
benchmark, measure and boundary), and later pushes leave it alone as long as
nothing new shows up. Only a regression nobody has seen yet reopens it.

The tool also cleans up after itself: once every alert is gone it resolves
the thread on its own, and a green first run creates the thread already
resolved, so it never gets in anyone's way.

If a plain comment from an earlier run without `--thread` is already on the
MR, it gets updated in place, since GitLab can't turn a comment into a
thread. Delete it once and the next run starts a proper thread.

## You need a real token

This is the annoying part, and it's a GitLab limitation, not ours:
`CI_JOB_TOKEN` can *read* MR comments but gets a 403 when creating or
updating one. Even the fine-grained job token permissions (GA since GitLab
18.3) only cover reading notes, not writing them — the feature request is
[gitlab-org/gitlab#464591](https://gitlab.com/gitlab-org/gitlab/-/issues/464591).

So: create a project access token with the `api` scope and put it in a masked
CI/CD variable called `BENCHER2GITLAB_TOKEN` (or `GITLAB_TOKEN`). The token is
only ever read from the environment, never from a flag, so it doesn't leak
into `ps` output or shell history. With `--thread` the token also has to be
allowed to resolve threads, so give it the Developer role.

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
--thread             post a resolvable thread instead of a plain comment
--timeout DUR        total HTTP timeout including retries (default 30s)
--dry-run            print the markdown to stdout and do nothing else
--version            print the version and exit
```

`--dry-run` is handy for checking what a report renders to:

```
./bencher2gitlab --report internal/report/testdata/full.json --dry-run
```

## Development

Plain Go, no dependencies — a handful of REST endpoints didn't seem worth a
GitLab SDK. The report structs were written against Bencher's Rust types
(`lib/bencher_json`) and their OpenAPI spec; parsing is deliberately lenient
so new upstream fields won't break anything.

Tests never talk to a real GitLab. `internal/gitlab/gitlabtest` fakes the
Notes and Discussions APIs on `httptest`, including pagination, thread
resolution, comments by other users, injected 429/5xx responses and the
job-token 403 behavior. The markdown output is covered by golden files:

```
go test ./...
go test ./internal/markdown -update   # regenerate golden files
```

When GitLab hiccups, requests are retried up to four times with exponential
backoff, and a `Retry-After` from the rate limiter is honored (unless it
points past the `--timeout`, in which case the tool gives up right away
instead of sleeping for nothing). Creating a comment is the one thing that
isn't retried after a 5xx or a dropped connection: GitLab may well have
saved it already, and a retry would leave you with two. A 429 is safe to
retry because the rate limiter turns the request away before it does
anything.
