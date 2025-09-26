package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	BugzillaURL   = "https://bugzilla.mozilla.org/rest/bug"
	Threshold     = 20
	DaysBack      = 7
	AuthorFilter  = "orangefactor@bots.tld"
	outputHTML    = "report.html"
)

var (
	reBlock = regexp.MustCompile(`(?s)## Repository breakdown:(.*?)## Table(.*?)$`)
	reNums  = regexp.MustCompile(`:\s*(\d+)`)
)

var components = []string{"AWSY", "mozperftest", "Performance", "Raptor", "Talos"}

type Bug struct {
	ID      int    `json:"id"`
	Summary string `json:"summary"`
	Flags   []struct {
		Name      string `json:"name"`
		Requestee string `json:"requestee"`
		Setter    string `json:"setter"`
	} `json:"flags,omitempty"`
	AssignedTo string `json:"assigned_to"`
}

type BugListResponse struct {
	Bugs []Bug `json:"bugs"`
}

type Comment struct {
	CreationTime string `json:"creation_time"`
	Author       string `json:"author"`
	Text         string `json:"text"`
}

type CommentBlock struct {
	Bugs map[string]struct {
		Comments []Comment `json:"comments"`
	} `json:"bugs"`
}

type Result struct {
	ID             int
	Link           string
	NumberFailures int
	Summary        string
	Platforms      []string
	BreakdownList  []string
	Needinfo       string
	GraphLink      string
	Assignee       string
}

type PermaBug struct {
	ID       int
	Link     string
	Summary  string
	Assignee string
	GraphURL string
	Needinfo string
}

func main() {
	var maxConcurrent int
	start := time.Now()
	defer func() {
		fmt.Printf("⏱ Report generated in %s\n", time.Since(start))
	}()
	// setup CLI flags for disabling the automatic HTML report opening in browser and allowing
	// user to specify number of concurrent fetches
	noOpen := flag.Bool("no-open", false, "Disable opening browser after generating report")
	concurrency := flag.Int("concurrency", 15, "Maximum number of concurrent Bugzilla fetches")
	flag.Parse()
	maxConcurrent = *concurrency

	fmt.Println("Generating Bugzilla report...")
	lastWeek := time.Now().AddDate(0, 0, -DaysBack)

	// Intermittent bugs (existing behavior)
	interBugs := fetchIntermittentBugs()
	results := analyzeAll(interBugs, lastWeek)

	// Perma bugs (new)
	permas := fetchPermaBugs()

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

// ===================== Fetchers =====================

func fetchIntermittentBugs() []Bug {
	params := url.Values{}
	params.Set("product", "Testing")
	params.Set("keywords", "intermittent-failure")
	params.Set("keywords_type", "allwords")
	params.Set("resolution", "---")
	params.Set("include_fields", "id,summary,flags,assigned_to")

	for _, c := range components {
		params.Add("component", c)
	}
	params.Set("include_fields", "id,summary,flags")

	resp, err := http.Get(BugzillaURL + "?" + params.Encode())
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
	return out.Bugs
}

func fetchPermaBugs() []PermaBug {
	params := url.Values{}
	params.Set("product", "Testing")
	params.Set("resolution", "---")
	params.Set("short_desc", "Perma")
	params.Set("short_desc_type", "allwordssubstr")
	params.Set("last_change_time", time.Now().AddDate(0, 0, -DaysBack).Format("2006-01-02"))
	params.Set("include_fields", "id,summary,assigned_to,flags")

	for _, c := range components {
		params.Add("component", c)
	}

	resp, err := http.Get(BugzillaURL + "?" + params.Encode())
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
		start := time.Now().AddDate(0, 0, -DaysBack).Format("2006-01-02")
		end := time.Now().Format("2006-01-02")
		graphURL := fmt.Sprintf(
			"https://treeherder.mozilla.org/intermittent-failures/bugdetails?startday=%s&endday=%s&tree=all&bug=%d",
			start, end, b.ID,
		)
		permas = append(permas, PermaBug{
			ID:       b.ID,
			Link:     fmt.Sprintf("https://bugzilla.mozilla.org/show_bug.cgi?id=%d", b.ID),
			Summary:  b.Summary,
			Assignee: assignee,
			GraphURL: graphURL,
			Needinfo: ni,
		})
	}
	return permas
}

// ===================== Analyzer (existing behavior) =====================

func analyzeAll(bugs []Bug, lastWeek time.Time) []Result {
	if len(bugs) == 0 {
		return nil
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	results := map[int]Result{}
	sema := make(chan struct{}, maxConcurrent)

	for _, bug := range bugs {
		wg.Add(1)
		sema <- struct{}{}

		go func(b Bug) {
			defer wg.Done()
			defer func() { <-sema }()

			if res := analyzeBug(b, lastWeek); res != nil {
				mu.Lock()
				results[b.ID] = *res
				mu.Unlock()
			}
		}(bug)
	}
	wg.Wait()

	// flatten + sort
	flat := make([]Result, 0, len(results))
	for _, v := range results {
		flat = append(flat, v)
	}
	sort.Slice(flat, func(i, j int) bool {
		return flat[i].NumberFailures > flat[j].NumberFailures
	})
	return flat
}

func analyzeBug(bug Bug, cutoff time.Time) *Result {
	url := fmt.Sprintf("%s/%d/comment", BugzillaURL, bug.ID)
	resp, err := http.Get(url)
	if err != nil {
		return nil
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("warning: error closing body: %v", err)
		}
	}()

	body, _ := io.ReadAll(resp.Body)
	var cb CommentBlock
	_ = json.Unmarshal(body, &cb)

	entry, ok := cb.Bugs[strconv.Itoa(bug.ID)]
	if !ok {
		return nil
	}

	max := 0
	var breakdownLines []string
	var platforms []string

	for i := len(entry.Comments) - 1; i >= 0; i-- {
		c := entry.Comments[i]
		t, err := time.Parse(time.RFC3339, c.CreationTime)
		if err != nil || t.Before(cutoff) || c.Author != AuthorFilter {
			continue
		}

		match := reBlock.FindStringSubmatch(c.Text)
		if len(match) < 3 {
			continue
		}
		repoBlock := match[1]
		platformBlock := match[2]

		total := 0
		for _, m := range reNums.FindAllStringSubmatch(repoBlock, -1) {
			val, _ := strconv.Atoi(m[1])
			total += val
		}

		if total > max {
			max = total
			breakdownLines = breakdownFrom(repoBlock)
			platforms = platformsFrom(platformBlock)
		}
	}

	if max >= Threshold {
		ni := ""
		for _, flag := range bug.Flags {
			if flag.Name == "needinfo" && flag.Requestee != "" {
				ni = flag.Requestee
				break
			}
		}
		// Get orange factor graph for last 7 days
		start := time.Now().AddDate(0, 0, -7).Format("2006-01-02")
		end := time.Now().Format("2006-01-02")
		graphLink := fmt.Sprintf("https://treeherder.mozilla.org/intermittent-failures/bugdetails?startday=%s&endday=%s&tree=all&bug=%d", start, end, bug.ID)

		// get assignee
		assigned := bug.AssignedTo
		if assigned == "nobody@mozilla.org" || assigned == "" {
			assigned = ""
		}

		return &Result{
			ID:             bug.ID,
			Link:           fmt.Sprintf("https://bugzilla.mozilla.org/show_bug.cgi?id=%d", bug.ID),
			NumberFailures: max,
			Summary:        bug.Summary,
			Platforms:      platforms,
			BreakdownList:  breakdownLines,
			Needinfo:       ni,
			GraphLink:      graphLink,
			Assignee:       assigned,
		}
	}
	return nil
}

func breakdownFrom(repoBlock string) []string {
	lines := []string{}
	for _, line := range strings.Split(repoBlock, "\n") {
		clean := strings.TrimSpace(line)
		if strings.HasPrefix(clean, "*") {
			clean = strings.TrimSpace(strings.TrimPrefix(clean, "*"))
		}
		if clean != "" {
			lines = append(lines, clean)
		}
	}
	return lines
}

func platformsFrom(platformBlock string) []string {
	plats := []string{}
	for _, line := range strings.Split(platformBlock, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if (strings.Contains(trimmed, "android") ||
			strings.Contains(trimmed, "linux") ||
			strings.Contains(trimmed, "macos") ||
			strings.Contains(trimmed, "win")) &&
			!strings.Contains(trimmed, "|") {
			// Strip leading markdown bullet if present
			clean := strings.TrimSpace(trimmed)
			if strings.HasPrefix(clean, "*") {
				clean = strings.TrimSpace(strings.TrimPrefix(clean, "*"))
			}
			plats = append(plats, clean)
		}
	}
	return plats
}

// ===================== HTML =====================

func writeHTMLReport(results []Result, permas []PermaBug) {
	tmpl := `
<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><title>Bugzilla Report</title>
<style>
body { font-family: sans-serif; padding: 1em; }
h2 { margin: .8em 0 .4em; }
ul.buglist { list-style: disc; padding-left: 1em; margin: 0; }
ul.details { list-style: circle; padding-left: 1.5em; margin-top: 0.25em; margin-bottom: 0; }
ul.subdetails { list-style: square; padding-left: 2em; margin: 0; }
.section { margin-top: 12px; }
</style>
</head><body>

<p style="font-size: 0.9em; color: #666;">Last updated: {{.Generated}}</p>
<h2>🟧 Intermittent Failures</h2>
<ul class="buglist">
{{range .Intermittents}}
<li><a href="{{.Link}}" target="_blank">Bug {{.ID}} - {{.Summary}}</a>
  <ul class="details">
    <li>(<a href="{{.GraphLink}}" target="_blank">Orange Factor Graph</a>)</li>
    <li>{{.NumberFailures}} Failures</li>
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

{{if .Permas}}
  <div class="section">
    <h2>🟥 Perma Failures</h2>
    <ul class="buglist">
      {{range .Permas}}
        <li>
          <a href="{{.Link}}" target="_blank">Bug {{.ID}} - {{.Summary}}</a>
          <ul class="details">
            <li>(<a href="{{.GraphURL}}" target="_blank">Orange Factor Graph</a>)</li>
            {{if .Assignee}}<li><b>Assigned To</b>: {{.Assignee}}</li>{{end}}
            {{if .Needinfo}}<li><b>NEEDINFO</b>: {{.Needinfo}}</li>{{end}}
          </ul>
        </li>
      {{end}}
    </ul>
  </div>
{{end}}

</body></html>`

	// Prepare data for template
	data := struct {
		Intermittents []Result
		Permas        []PermaBug
		Generated     string
	}{
		Intermittents: results,
		Permas:        permas,
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
