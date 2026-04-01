package main

import (
	_ "embed"
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
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	BugzillaURL      = "https://bugzilla.mozilla.org/rest/bug"
	TreeherderURL    = "https://treeherder.mozilla.org/api"
	outputHTML       = "report.html"
	taskTimeoutBugID = 1809667
)

var perfTestKeywords = []string{"browsertime", "talos", "perftest", "awsy"}

var (
	threshold int
	daysBack  int
)

var (
	maxConcurrent  int
	bugzillaBase   = BugzillaURL
	treeherderBase = TreeherderURL
)

//go:embed template.html
var reportTemplate string

var components = []string{"AWSY", "Condprofile", "mozperftest", "Performance", "Raptor", "Talos"}

type Bug struct {
	ID           int    `json:"id"`
	Summary      string `json:"summary"`
	Component    string `json:"component"`
	CreationTime string `json:"creation_time"`
	Flags        []struct {
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
	Age            string
	Rate           string
	Trend          string
	TwoDay         int
	TwoDayRate     string
	Platforms      []string
	BreakdownList  []string
	Needinfo       string
	GraphLink      string
	Assignee       string
}

type PermaBug struct {
	ID              int
	Link            string
	Summary         string
	Component       string
	Age             string
	Assignee        string
	GraphLink       string
	Needinfo        string
	NumberFailures  int
	TwoDayFailures  int
	Platforms       []string
	BreakdownList   []string
	TwoDayPlatforms []string
	TwoDayBreakdown []string
}

type TaskTimeoutReport struct {
	Link           string
	GraphLink      string
	PerfFailures   int
	SuiteBreakdown []string
	Platforms      []string
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

func bugAge(creationTime string) string {
	t, err := time.Parse(time.RFC3339, creationTime)
	if err != nil {
		return ""
	}
	days := int(time.Since(t).Hours() / 24)
	return fmt.Sprintf("%d days", days)
}

func computeTrend(current, previous int) string {
	if previous == 0 {
		return "🆕"
	}
	delta := current - previous
	if delta > 0 {
		return fmt.Sprintf("↑ +%d", delta)
	}
	if delta < 0 {
		return fmt.Sprintf("↓ %d", delta)
	}
	return ""
}

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

type THDailyCount struct {
	TestRuns     int `json:"test_runs"`
	FailureCount int `json:"failure_count"`
}

func main() {
	start := time.Now()
	defer func() {
		fmt.Printf("⏱ Report generated in %s\n", time.Since(start))
	}()
	// setup CLI flags for disabling the automatic HTML report opening in browser and allowing
	// user to specify number of concurrent fetches
	noOpen := flag.Bool("no-open", false, "Disable opening browser after generating report")
	concurrency := flag.Int("concurrency", 10, "Maximum number of concurrent Treeherder breakdown fetches")
	flag.IntVar(&threshold, "threshold", 20, "Minimum failure count to include a bug")
	flag.IntVar(&daysBack, "days", 7, "Number of days back to query")
	flag.Parse()
	maxConcurrent = *concurrency

	fmt.Println("Generating PerfTest triage report...")

	startDay := time.Now().AddDate(0, 0, -daysBack).Format("2006-01-02")
	endDay := time.Now().Format("2006-01-02")
	prevStartDay := time.Now().AddDate(0, 0, -daysBack*2).Format("2006-01-02")
	twoDayStart := time.Now().AddDate(0, 0, -2).Format("2006-01-02")
	var interBugs []Bug
	var rawPermas []PermaBug
	var currentCounts, prevCounts, twoDayCounts map[int]int
	var wg sync.WaitGroup
	wg.Add(5)
	go func() { defer wg.Done(); interBugs = fetchIntermittentBugs() }()
	go func() { defer wg.Done(); rawPermas = fetchPermaBugs(startDay, endDay) }()
	go func() { defer wg.Done(); currentCounts = fetchTreeherderCounts(startDay, endDay) }()
	go func() { defer wg.Done(); prevCounts = fetchTreeherderCounts(prevStartDay, startDay) }()
	go func() { defer wg.Done(); twoDayCounts = fetchTreeherderCounts(twoDayStart, endDay) }()
	wg.Wait()

	var results []Result
	var permas []PermaBug
	var taskTimeout *TaskTimeoutReport
	var wg2 sync.WaitGroup
	wg2.Add(3)
	go func() {
		defer wg2.Done()
		results = analyzeAll(interBugs, startDay, endDay, currentCounts, prevCounts, twoDayStart, twoDayCounts)
	}()
	go func() {
		defer wg2.Done()
		permas = enrichPermas(rawPermas, startDay, endDay, twoDayStart, currentCounts, twoDayCounts)
	}()
	go func() {
		defer wg2.Done()
		taskTimeout = analyzeTaskTimeout(startDay, endDay)
	}()
	wg2.Wait()

	if len(results) == 0 && len(permas) == 0 {
		fmt.Println("No matching bugs found.")
		return
	}

	writeHTMLReport(results, permas, taskTimeout)
	fmt.Println("✅ Report written to", outputHTML)
	if !*noOpen {
		openInBrowser(outputHTML)
	}
}

var httpClient = &http.Client{Timeout: 60 * time.Second}
var retrySleep = func(d time.Duration) { time.Sleep(d) }

func get(u string) (*http.Response, error) {
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			retrySleep(time.Duration(1<<uint(attempt-1)) * time.Second)
		}
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "mozilla-perftest-report/1.0")

		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("request failed (attempt %d/3): %v", attempt+1, err)
			continue
		}
		if resp.StatusCode >= 500 {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("status %s", resp.Status)
			log.Printf("server error (attempt %d/3): %s", attempt+1, resp.Status)
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

// ===================== Fetchers =====================

func fetchIntermittentBugs() []Bug {
	params := url.Values{}
	params.Set("product", "Testing")
	params.Set("keywords", "intermittent-failure")
	params.Set("keywords_type", "allwords")
	params.Set("resolution", "---")
	params.Set("include_fields", "id,summary,component,creation_time,flags,assigned_to")

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
	params.Set("include_fields", "id,summary,component,creation_time,assigned_to,flags")
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
			Age:       bugAge(b.CreationTime),
			Assignee:  assignee,
			GraphLink: graphURL,
			Needinfo:  ni,
		})
	}
	return permas
}

func enrichPermas(permas []PermaBug, start, end, twoDayStart string, counts, twoDayCounts map[int]int) []PermaBug {
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
			twoDayBreakdowns, twoDayPlatforms := fetchTreeherderBreakdown(bug.ID, twoDayStart, end)
			mu.Lock()
			permas[idx].NumberFailures = counts[bug.ID]
			permas[idx].TwoDayFailures = twoDayCounts[bug.ID]
			permas[idx].BreakdownList = breakdowns
			permas[idx].Platforms = platforms
			permas[idx].TwoDayBreakdown = twoDayBreakdowns
			permas[idx].TwoDayPlatforms = twoDayPlatforms
			mu.Unlock()
		}(i, p)
	}
	wg.Wait()

	filtered := permas[:0]
	for _, p := range permas {
		if len(p.BreakdownList) > 0 || len(p.Platforms) > 0 {
			filtered = append(filtered, p)
		}
	}
	return filtered
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

func fetchFailureRate(bugID int, start, end string) string {
	u := fmt.Sprintf("%s/failurecount/?startday=%s&endday=%s&tree=all&bug=%d", treeherderBase, start, end, bugID)
	resp, err := get(u)
	if err != nil {
		return ""
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("warning: error closing body: %v", err)
		}
	}()

	var days []THDailyCount
	if err := json.NewDecoder(resp.Body).Decode(&days); err != nil {
		return ""
	}

	var totalRuns, totalFailures int
	for _, d := range days {
		totalRuns += d.TestRuns
		totalFailures += d.FailureCount
	}
	if totalRuns == 0 {
		return ""
	}
	return fmt.Sprintf("%.1f%%", float64(totalFailures)/float64(totalRuns)*100)
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

func analyzeAll(bugs []Bug, start, end string, counts, prevCounts map[int]int, twoDayStart string, twoDayCounts map[int]int) []Result {
	if len(bugs) == 0 {
		return nil
	}

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
			rate := fetchFailureRate(b.ID, start, end)

			twoDayCount := twoDayCounts[b.ID]
			twoDayRate := ""
			if twoDayCount > 0 {
				twoDayRate = fetchFailureRate(b.ID, twoDayStart, end)
			}

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
				Age:            bugAge(b.CreationTime),
				Rate:           rate,
				Trend:          computeTrend(counts[b.ID], prevCounts[b.ID]),
				TwoDay:         twoDayCount,
				TwoDayRate:     twoDayRate,
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

// ===================== Task Timeout =====================

func analyzeTaskTimeout(start, end string) *TaskTimeoutReport {
	u := fmt.Sprintf("%s/failuresbybug/?startday=%s&endday=%s&tree=all&bug=%d", treeherderBase, start, end, taskTimeoutBugID)
	resp, err := get(u)
	if err != nil {
		log.Printf("fetch task timeout breakdown: %v", err)
		return nil
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("warning: error closing body: %v", err)
		}
	}()

	var failures []THJobFailure
	if err := json.NewDecoder(resp.Body).Decode(&failures); err != nil {
		log.Printf("decode task timeout breakdown: %v", err)
		return nil
	}

	var perf []THJobFailure
	for _, f := range failures {
		suite := strings.ToLower(f.TestSuite)
		for _, kw := range perfTestKeywords {
			if strings.Contains(suite, kw) {
				perf = append(perf, f)
				break
			}
		}
	}

	if len(perf) == 0 {
		return nil
	}

	suiteCounts := map[string]int{}
	platformCounts := map[string]int{}
	for _, f := range perf {
		suiteCounts[f.TestSuite]++
		if p := normalizePlatform(f.Platform); p != "" {
			platformCounts[p]++
		}
	}

	var suiteBreakdown []string
	for suite, count := range suiteCounts {
		suiteBreakdown = append(suiteBreakdown, fmt.Sprintf("%s: %d", suite, count))
	}
	sort.Strings(suiteBreakdown)

	var platforms []string
	for p, count := range platformCounts {
		platforms = append(platforms, fmt.Sprintf("%s: %d", p, count))
	}
	sort.Strings(platforms)

	return &TaskTimeoutReport{
		Link: fmt.Sprintf("https://bugzilla.mozilla.org/show_bug.cgi?id=%d", taskTimeoutBugID),
		GraphLink: fmt.Sprintf(
			"https://treeherder.mozilla.org/intermittent-failures/bugdetails?startday=%s&endday=%s&tree=all&bug=%d",
			start, end, taskTimeoutBugID,
		),
		PerfFailures:   len(perf),
		SuiteBreakdown: suiteBreakdown,
		Platforms:      platforms,
	}
}

// ===================== HTML =====================

type reportData struct {
	Intermittents []ComponentGroup[Result]
	Permas        []ComponentGroup[PermaBug]
	TaskTimeout   *TaskTimeoutReport
	Generated     string
	DaysBack      int
}

func writeHTMLReport(results []Result, permas []PermaBug, taskTimeout *TaskTimeoutReport) {
	tmpl := reportTemplate

	data := reportData{
		Intermittents: groupByComponent(results, components),
		Permas:        groupByComponent(permas, components),
		TaskTimeout:   taskTimeout,
		Generated:     time.Now().UTC().Format("2006-01-02 15:04 MST"),
		DaysBack:      daysBack,
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

	if err := renderHTML(f, tmpl, data); err != nil {
		log.Fatalf("template exec: %v", err)
	}
}

func renderHTML(w io.Writer, tmpl string, data any) error {
	t := template.Must(template.New("report").Parse(tmpl))
	return t.Execute(w, data)
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
