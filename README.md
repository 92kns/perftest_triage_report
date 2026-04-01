# Perftest Triage Report Generator

A Go CLI tool that automates the generation of weekly performance test triage reports by querying Bugzilla and Treeherder for intermittent failures, perma failures, and generic task timeouts affecting perf tests.

Useful for perftest triage sessions where engineers need a concise and accurate snapshot of the week's flakiest or most problematic bugs.

---

## Report Sections

- 🟧 **Intermittent Failures** — open bugs with `intermittent-failure` keyword, filtered to those meeting the failure threshold
- 🟥 **Perma Failures** — open bugs with "Perma" in the title, active in the report window
- 🔶 **Generic Task Timeout** — perf-test failures (browsertime, talos, perftest, awsy) from [Bug 1809667](https://bugzilla.mozilla.org/show_bug.cgi?id=1809667), always reported separately

All sections are grouped by component: AWSY, Condprofile, mozperftest, Performance, Raptor, Talos.

---

## Features

- **Dual time windows** — primary window (default 7d) and a 2-day snapshot for each bug, showing recent activity alongside the weekly view
- **Failure rate** — expressed as failures per push to the tree (sourced from Treeherder `/failurecount/`)
- **Week-over-week trend** — `↑ +N` / `↓ N` comparing the current 7d window against the prior 7d window
- **Platform and repository breakdown** — for both 7d and 2d windows
- **Suite breakdown** — for the Generic Task Timeout section
- **Bug age**, **Assigned To**, and **NEEDINFO** tracking
- **OrangeFactor graph links** per bug
- Daily report published at 0900 UTC to GitHub Pages

---

## Usage

### Run locally

```bash
go run main.go
```

Generates `report.html` and opens it in your browser.

### CLI flags

| Flag            | Default | Description                                    |
|-----------------|---------|------------------------------------------------|
| `--no-open`     | false   | Do not open the browser after report generates |
| `--concurrency` | 10      | Max concurrent Treeherder API calls            |
| `--threshold`   | 20      | Minimum failure count to include a bug         |
| `--days`        | 7       | Primary window size in days                    |

---

## Development

Run tests:

```bash
go test ./...
go test -race ./...
```

Enable the pre-push hook that runs tests before each push:

```bash
git config core.hooksPath .githooks
```

---

## Build

```bash
go build -o perftest-report main.go
./perftest-report --no-open
```

---

## Output

Latest published report: https://92kns.github.io/perftest_triage_report/

---

## Credits

- Original Python script by [@florinbilt](https://github.com/florinbilt)
- Developed and maintained by [@kshampur](https://github.com/92kns)

---

## License

MIT License. See `LICENSE` file.
