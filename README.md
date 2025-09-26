# Perftest Triage Report Generator

A Go CLI tool that automates the generation of weekly performance test triage reports by querying Bugzilla for:

- 🟧 Intermittent test failures with high orangefactor counts (>=20)
- 🟥 Recent "Perma" bugs based on title substring
- Repository breakdown
- Assigned developer and NEEDINFO status
- Outputs a ready-to-copy HTML report

Useful for perftest triage sessions where engineers need a concise and accurate snapshot of the week’s flakiest or most problematic bugs.

---

## Current Features

- **Intermittent Bugs** (over threshold)
- **Perma Bugs** (identified by title)
- **OrangeFactor Graphs** (last 7 days)
- Platform + Repository breakdown parsing
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
| `--concurrency`  | 15      | Max concurrent Bugzilla API calls                 |

---

## Build

To compile a standalone binary:

```bash
go build -o bugzilla-report main.go
./bugzilla-report --no-open
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

- Trend reporting/detection via last 2-3 comments
- Archiving weekly reports in repo history
- Calls to treeherder database
- Breakdown by platform

---

## Credits

- Original Python script by [@florinbilt](https://github.com/florinbilt)
- Developed and maintained by [@kshampur](https://github.com/92kns)

---

## License

MIT License. See `LICENSE` file.
