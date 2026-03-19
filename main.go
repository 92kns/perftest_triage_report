package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	BugzillaURL   = "https://bugzilla.mozilla.org/rest/bug"
	TreeherderURL = "https://treeherder.mozilla.org/api"
	outputHTML    = "report.html"
)

var (
	threshold int
	daysBack  int
)

var (
	maxConcurrent  int
	bugzillaBase   = BugzillaURL
	treeherderBase = TreeherderURL
)

var components = []string{"AWSY", "mozperftest", "Performance", "Raptor", "Talos"}

type Bug struct {
	ID        int    `json:"id"`
	Summary   string `json:"summary"`
	Component string `json:"component"`
	Flags     []struct {
		Name      string `json:"name"`
		Requestee string `json:"requestee"`
	} `json:"flags,omitempty"`
	AssignedTo string `json:"assigned_to"`
}

type BugListResponse struct {
	Bugs []Bug `json:"bugs"`
}

type Result struct {
	ID             int
	Link           string
	NumberFailures int
	Summary        string
	Component      string
	Platforms      []string
	BreakdownList  []string
	Needinfo       string
	GraphLink      string
	Assignee       string
}

type PermaBug struct {
	ID            int
	Link          string
	Summary       string
	Component     string
	Assignee      string
	GraphLink     string
	Needinfo      string
	Platforms     []string
	BreakdownList []string
}

type ComponentGroup[T any] struct {
	Name string
	Bugs []T
}

type hasComponent interface {
	component() string
}

func (r Result) component() string   { return r.Component }
func (p PermaBug) component() string { return p.Component }

func groupByComponent[T hasComponent](items []T, order []string) []ComponentGroup[T] {
	m := map[string][]T{}
	for _, item := range items {
		c := item.component()
		m[c] = append(m[c], item)
	}
	var groups []ComponentGroup[T]
	for _, name := range order {
		if bugs, ok := m[name]; ok {
			groups = append(groups, ComponentGroup[T]{Name: name, Bugs: bugs})
		}
	}
	return groups
}

type THFailure struct {
	BugID    *int `json:"bug_id"`
	BugCount int  `json:"bug_count"`
}

type THJobFailure struct {
	Platform  string `json:"platform"`
	Tree      string `json:"tree"`
	TestSuite string `json:"test_suite"`
}

func main() {
	start := time.Now()
	defer func() {
		fmt.Printf("⏱ Report generated in %s\n", time.Since(start))
	}()
	// setup CLI flags for disabling the automatic HTML report opening in browser and allowing
	// user to specify number of concurrent fetches
	noOpen := flag.Bool("no-open", false, "Disable opening browser after generating report")
	concurrency := flag.Int("concurrency", 5, "Maximum number of concurrent Treeherder breakdown fetches")
	flag.IntVar(&threshold, "threshold", 20, "Minimum failure count to include a bug")
	flag.IntVar(&daysBack, "days", 7, "Number of days back to query")
	flag.Parse()
	maxConcurrent = *concurrency

	fmt.Println("Generating PerfTest triage report...")

	startDay := time.Now().AddDate(0, 0, -daysBack).Format("2006-01-02")
	endDay := time.Now().Format("2006-01-02")

	var interBugs []Bug
	var rawPermas []PermaBug
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); interBugs = fetchIntermittentBugs() }()
	go func() { defer wg.Done(); rawPermas = fetchPermaBugs(startDay, endDay) }()
	wg.Wait()

	results := analyzeAll(interBugs, startDay, endDay)
	permas := enrichPermas(rawPermas, startDay, endDay)

	if len(results) == 0 && len(permas) == 0 {
		fmt.Println("No matching bugs found.")
		return
	}

	writeHTMLReport(results, permas)
	fmt.Println("✅ Report written to", outputHTML)
	if !*noOpen {
		openInBrowser(outputHTML)
	}
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

func get(u string) (*http.Response, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "mozilla-perftest-report/1.0")
	return httpClient.Do(req)
}

// ===================== Fetchers =====================

func fetchIntermittentBugs() []Bug {
	params := url.Values{}
	params.Set("product", "Testing")
	params.Set("keywords", "intermittent-failure")
	params.Set("keywords_type", "allwords")
	params.Set("resolution", "---")
	params.Set("include_fields", "id,summary,component,flags,assigned_to")

	for _, c := range components {
		params.Add("component", c)
	}

	resp, err := get(bugzillaBase + "?" + params.Encode())
	if err != nil {
		log.Fatalf("fetch intermittents failed: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("warning: error closing body: %v", err)
		}
	}()

	var out BugListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		log.Fatalf("bad intermittent bug JSON: %v", err)
	}
	filtered := make([]Bug, 0, len(out.Bugs))
	for _, b := range out.Bugs {
		if !strings.Contains(strings.ToLower(b.Summary), "perma") {
			filtered = append(filtered, b)
		}
	}
	return filtered
}

func fetchPermaBugs(start, end string) []PermaBug {
	params := url.Values{}
	params.Set("product", "Testing")
	params.Set("resolution", "---")
	params.Set("short_desc", "Perma")
	params.Set("short_desc_type", "allwordssubstr")
	params.Set("last_change_time", start)
	params.Set("include_fields", "id,summary,component,assigned_to,flags")
	params.Set("keywords", "intermittent-failure")

	for _, c := range components {
		params.Add("component", c)
	}

	resp, err := get(bugzillaBase + "?" + params.Encode())
	if err != nil {
		log.Fatalf("fetch failed: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("warning: error closing body: %v", err)
		}
	}()

	var out BugListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		log.Fatalf("bad bug JSON: %v", err)
	}

	var permas []PermaBug
	for _, b := range out.Bugs {
		ni := ""
		for _, flag := range b.Flags {
			if flag.Name == "needinfo" && flag.Requestee != "" {
				ni = flag.Requestee
				break
			}
		}

		assignee := b.AssignedTo
		if assignee == "nobody@mozilla.org" {
			assignee = ""
		}
		graphURL := fmt.Sprintf(
			"https://treeherder.mozilla.org/intermittent-failures/bugdetails?startday=%s&endday=%s&tree=all&bug=%d",
			start, end, b.ID,
		)
		permas = append(permas, PermaBug{
			ID:        b.ID,
			Link:      fmt.Sprintf("https://bugzilla.mozilla.org/show_bug.cgi?id=%d", b.ID),
			Summary:   b.Summary,
			Component: b.Component,
			Assignee:  assignee,
			GraphLink: graphURL,
			Needinfo:  ni,
		})
	}
	return permas
}

func enrichPermas(permas []PermaBug, start, end string) []PermaBug {
	var wg sync.WaitGroup
	var mu sync.Mutex
	sema := make(chan struct{}, maxConcurrent)

	for i, p := range permas {
		wg.Add(1)
		sema <- struct{}{}

		go func(idx int, bug PermaBug) {
			defer wg.Done()
			defer func() { <-sema }()

			breakdowns, platforms := fetchTreeherderBreakdown(bug.ID, start, end)
			mu.Lock()
			permas[idx].BreakdownList = breakdowns
			permas[idx].Platforms = platforms
			mu.Unlock()
		}(i, p)
	}
	wg.Wait()
	return permas
}

// ===================== Treeherder =====================

func fetchTreeherderCounts(start, end string) map[int]int {
	u := fmt.Sprintf("%s/failures/?startday=%s&endday=%s&tree=all", treeherderBase, start, end)
	resp, err := get(u)
	if err != nil {
		log.Fatalf("fetch treeherder counts: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("warning: error closing body: %v", err)
		}
	}()
	if resp.StatusCode != 200 {
		log.Fatalf("treeherder counts: unexpected status %s", resp.Status)
	}

	var counts []THFailure
	if err := json.NewDecoder(resp.Body).Decode(&counts); err != nil {
		log.Fatalf("decode treeherder counts: %v", err)
	}

	m := make(map[int]int, len(counts))
	for _, c := range counts {
		if c.BugID != nil {
			m[*c.BugID] = c.BugCount
		}
	}
	return m
}

func fetchTreeherderBreakdown(bugID int, start, end string) (breakdowns []string, platforms []string) {
	u := fmt.Sprintf("%s/failuresbybug/?startday=%s&endday=%s&tree=all&bug=%d", treeherderBase, start, end, bugID)
	resp, err := get(u)
	if err != nil {
		return nil, nil
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("warning: error closing body: %v", err)
		}
	}()

	var failures []THJobFailure
	if err := json.NewDecoder(resp.Body).Decode(&failures); err != nil {
		return nil, nil
	}
	return aggregateBreakdown(failures)
}

func aggregateBreakdown(failures []THJobFailure) (breakdowns []string, platforms []string) {
	treeCounts := map[string]int{}
	platformCounts := map[string]int{}
	for _, f := range failures {
		treeCounts[f.Tree]++
		platformStr := f.Platform
		if strings.EqualFold(platformStr, "toolchains") {
			platformStr = f.TestSuite
		}
		if p := normalizePlatform(platformStr); p != "" {
			platformCounts[p]++
		}
	}

	for tree, count := range treeCounts {
		breakdowns = append(breakdowns, fmt.Sprintf("%s: %d", tree, count))
	}
	sort.Strings(breakdowns)

	for p, count := range platformCounts {
		platforms = append(platforms, fmt.Sprintf("%s: %d", p, count))
	}
	sort.Strings(platforms)
	return
}

func normalizePlatform(platform string) string {
	p := strings.ToLower(platform)
	if p == "" {
		return ""
	}
	base := strings.SplitN(p, "-", 2)[0]
	switch {
	case strings.HasPrefix(base, "android"):
		parts := strings.Split(p, "-")
		if len(parts) >= 3 {
			return strings.Join(parts[:3], "-") // e.g. android-hw-p6, android-hw-a55
		}
		return "android"
	case strings.HasPrefix(base, "linux"):
		return base // e.g. linux1804, linux2404
	case strings.HasPrefix(base, "macosx"), strings.HasPrefix(base, "osx"):
		return base // e.g. macosx1470, macosx1500
	case strings.HasPrefix(base, "win"):
		return base // e.g. windows11
	}
	// Fallback for strings like "toolchain-linux64-custom-car"
	switch {
	case strings.Contains(p, "android"):
		return "android"
	case strings.Contains(p, "linux"):
		return "linux"
	case strings.Contains(p, "macos") || strings.Contains(p, "osx"):
		return "macos"
	case strings.Contains(p, "win"):
		return "windows"
	}
	return platform
}

// ===================== Analyzer =====================

func analyzeAll(bugs []Bug, start, end string) []Result {
	if len(bugs) == 0 {
		return nil
	}

	counts := fetchTreeherderCounts(start, end)

	var qualifying []Bug
	for _, b := range bugs {
		if counts[b.ID] >= threshold {
			qualifying = append(qualifying, b)
		}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var results []Result
	sema := make(chan struct{}, maxConcurrent)

	for _, bug := range qualifying {
		wg.Add(1)
		sema <- struct{}{}

		go func(b Bug) {
			defer wg.Done()
			defer func() { <-sema }()

			breakdowns, platforms := fetchTreeherderBreakdown(b.ID, start, end)

			ni := ""
			for _, flag := range b.Flags {
				if flag.Name == "needinfo" && flag.Requestee != "" {
					ni = flag.Requestee
					break
				}
			}

			assigned := b.AssignedTo
			if assigned == "nobody@mozilla.org" || assigned == "" {
				assigned = ""
			}

			graphLink := fmt.Sprintf(
				"https://treeherder.mozilla.org/intermittent-failures/bugdetails?startday=%s&endday=%s&tree=all&bug=%d",
				start, end, b.ID,
			)

			mu.Lock()
			results = append(results, Result{
				ID:             b.ID,
				Link:           fmt.Sprintf("https://bugzilla.mozilla.org/show_bug.cgi?id=%d", b.ID),
				NumberFailures: counts[b.ID],
				Summary:        b.Summary,
				Component:      b.Component,
				Platforms:      platforms,
				BreakdownList:  breakdowns,
				Needinfo:       ni,
				GraphLink:      graphLink,
				Assignee:       assigned,
			})
			mu.Unlock()
		}(bug)
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool {
		return results[i].NumberFailures > results[j].NumberFailures
	})
	return results
}

// ===================== HTML =====================

func writeHTMLReport(results []Result, permas []PermaBug) {
	tmpl := `
<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><title>PerfTest Triage Report</title>
<style>
body { font-family: sans-serif; padding: 1em; }
h2 { margin: .8em 0 .4em; }
h3 { margin: .6em 0 .3em; color: #555; font-size: 1em; }
ul.buglist { list-style: disc; padding-left: 1em; margin: 0; }
ul.details { list-style: circle; padding-left: 1.5em; margin-top: 0.25em; margin-bottom: 0; }
ul.subdetails { list-style: square; padding-left: 2em; margin: 0; }
.section { margin-top: 12px; }
.component-group { margin-top: 10px; }
</style>
</head><body>

<p style="font-size: 0.9em; color: #666; user-select: none;">
  Last updated: {{.Generated}} |
<a href="https://github.com/92kns/perftest_triage_report/issues" target="_blank" style="font-size: 0.9em;">
  🐞 File an issue on GitHub
</a>
</p>
<h2>🟧 Intermittent Failures</h2>
{{range .Intermittents}}
<div class="component-group">
  <h3>{{.Name}}</h3>
  <ul class="buglist">
  {{range .Bugs}}
  <li><a href="{{.Link}}" target="_blank">Bug {{.ID}} - {{.Summary}}</a>
    <ul class="details">
      <li><a href="{{.GraphLink}}" target="_blank">Orange Factor Graph 📈</a></li>
      <li><b>{{.NumberFailures}}</b> Failures</li>
      {{if .Platforms}}
        <li>Platforms:
          <ul class="subdetails">{{range .Platforms}}<li>{{.}}</li>{{end}}</ul>
        </li>
      {{end}}
      {{if .BreakdownList}}
        <li>Repository Breakdown:
          <ul class="subdetails">{{range .BreakdownList}}<li>{{.}}</li>{{end}}</ul>
        </li>
      {{end}}
      {{if .Assignee}}<li><b>Assigned To</b>: {{.Assignee}}</li>{{end}}
      {{if .Needinfo}}<li><b>NEEDINFO</b>: {{.Needinfo}}</li>{{end}}
    </ul>
  </li>
  {{end}}
  </ul>
</div>
{{end}}

{{if .Permas}}
  <div class="section">
    <h2>🟥 Perma Failures</h2>
    {{range .Permas}}
    <div class="component-group">
      <h3>{{.Name}}</h3>
      <ul class="buglist">
        {{range .Bugs}}
        <li>
          <a href="{{.Link}}" target="_blank">Bug {{.ID}} - {{.Summary}}</a>
          <ul class="details">
            <li><a href="{{.GraphLink}}" target="_blank">Orange Factor Graph 📈</a></li>
            {{if .Platforms}}
              <li>Platforms:
                <ul class="subdetails">{{range .Platforms}}<li>{{.}}</li>{{end}}</ul>
              </li>
            {{end}}
            {{if .BreakdownList}}
              <li>Repository Breakdown:
                <ul class="subdetails">{{range .BreakdownList}}<li>{{.}}</li>{{end}}</ul>
              </li>
            {{end}}
            {{if .Assignee}}<li><b>Assigned To</b>: {{.Assignee}}</li>{{end}}
            {{if .Needinfo}}<li><b>NEEDINFO</b>: {{.Needinfo}}</li>{{end}}
          </ul>
        </li>
        {{end}}
      </ul>
    </div>
    {{end}}
  </div>
{{end}}

<script>
document.querySelectorAll('ul.subdetails li').forEach(el => {
  el.innerHTML = el.innerHTML.replace(/(:\s*)(\d+)/g, '$1<b>$2</b>');
});
</script>
</body></html>`

	data := struct {
		Intermittents []ComponentGroup[Result]
		Permas        []ComponentGroup[PermaBug]
		Generated     string
	}{
		Intermittents: groupByComponent(results, components),
		Permas:        groupByComponent(permas, components),
		Generated:     time.Now().UTC().Format("2006-01-02 15:04 MST"),
	}

	f, err := os.Create(outputHTML)
	if err != nil {
		log.Fatalf("create file: %v", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("warning: error closing file: %v", err)
		}
	}()

	t := template.Must(template.New("report").Parse(tmpl))
	if err := t.Execute(f, data); err != nil {
		log.Fatalf("template exec: %v", err)
	}
}

// ===================== Open in browser =====================

func openInBrowser(file string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", file)
	case "linux":
		cmd = exec.Command("xdg-open", file)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", file)
	default:
		fmt.Printf("Open %s manually in your browser.\n", file)
		return
	}
	_ = cmd.Start()
}
