# Perftest Triage Report Generator

A Go CLI tool that automates the generation of weekly performance test triage reports by querying Bugzilla and Treeherder for:

- 🟧 Intermittent test failures with high failure counts (>=20)
- 🟥 Recent "Perma" bugs based on title substring
- Repository and platform breakdown (via Treeherder API)
- Assigned developer and NEEDINFO status
- Outputs a ready-to-copy HTML report

Useful for perftest triage sessions where engineers need a concise and accurate snapshot of the week’s flakiest or most problematic bugs.

---

## Current Features

- **Intermittent Bugs** (over threshold)
- **Perma Bugs** (identified by title)
- **OrangeFactor Graphs** (last 7 days)
- Platform and repository breakdown (sourced directly from Treeherder API)
- Bugs grouped by component: AWSY, mozperftest, Performance, Raptor, Talos
- **Assigned To** and **NEEDINFO** tracking
- Generates `report.html`
- Daily report generated at 0900 UTC and published to pages

---

## Usage

### Run locally

```bash
go run main.go
```

Generates `report.html` and opens it in your browser.

### CLI flags

| Flag             | Default | Description                                        |
|------------------|---------|----------------------------------------------------|
| `--no-open`      | false   | Do not open the browser after report is generated |
| `--concurrency`  | 5       | Max concurrent Treeherder breakdown API calls     |

---

## Development Setup

To enable the pre-push hook that runs tests before each push:

```bash
git config core.hooksPath .githooks
```

---

## Build

To compile a standalone binary:

```bash
go build -o perftest-report main.go
./perftest-report --no-open
```

---

## Output Example

See the latest published report here:

https://92kns.github.io/perftest_triage_report/

HTML includes:
- Intermittent bug summaries with repo and platform breakdowns
- Links to OrangeFactor graphs
- Perma bugs from last 7 days

---

## Future Ideas

- Trend reporting/detection using Treeherder historical data
- Archiving weekly reports in repo history

---

## Credits

- Original Python script by [@florinbilt](https://github.com/florinbilt)
- Developed and maintained by [@kshampur](https://github.com/92kns)

---

## License

MIT License. See `LICENSE` file.
