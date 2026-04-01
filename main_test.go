package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestComputeTrend(t *testing.T) {
	tests := []struct {
		current, previous int
		want              string
	}{
		{50, 0, "🆕"},       // new this week
		{100, 60, "↑ +40"}, // spiking
		{30, 50, "↓ -20"},  // improving
		{50, 50, ""},       // no change
	}
	for _, tt := range tests {
		got := computeTrend(tt.current, tt.previous)
		if got != tt.want {
			t.Errorf("computeTrend(%d, %d) = %q, want %q", tt.current, tt.previous, got, tt.want)
		}
	}
}

func TestBugAge(t *testing.T) {
	tests := []struct {
		input    string
		wantDays int
	}{
		{time.Now().UTC().AddDate(0, 0, -137).Format(time.RFC3339), 137},
		{time.Now().UTC().AddDate(0, 0, -1).Format(time.RFC3339), 1},
		{time.Now().UTC().Format(time.RFC3339), 0},
		{"", -1}, // invalid — should return ""
	}

	for _, tt := range tests {
		got := bugAge(tt.input)
		if tt.wantDays == -1 {
			if got != "" {
				t.Errorf("bugAge(%q) = %q, want empty string", tt.input, got)
			}
			continue
		}
		want := fmt.Sprintf("%d days", tt.wantDays)
		if got != want {
			t.Errorf("bugAge(%q) = %q, want %q", tt.input, got, want)
		}
	}
}

func TestGroupByComponent(t *testing.T) {
	results := []Result{
		{ID: 1, Component: "Raptor", NumberFailures: 50},
		{ID: 2, Component: "Talos", NumberFailures: 30},
		{ID: 3, Component: "Raptor", NumberFailures: 80},
		{ID: 4, Component: "AWSY", NumberFailures: 20},
	}
	order := []string{"AWSY", "mozperftest", "Performance", "Raptor", "Talos"}
	groups := groupByComponent(results, order)

	// mozperftest and Performance have no bugs, should be omitted
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3", len(groups))
	}
	if groups[0].Name != "AWSY" || len(groups[0].Bugs) != 1 {
		t.Errorf("group 0: got %q with %d bugs, want AWSY with 1", groups[0].Name, len(groups[0].Bugs))
	}
	if groups[1].Name != "Raptor" || len(groups[1].Bugs) != 2 {
		t.Errorf("group 1: got %q with %d bugs, want Raptor with 2", groups[1].Name, len(groups[1].Bugs))
	}
	if groups[2].Name != "Talos" || len(groups[2].Bugs) != 1 {
		t.Errorf("group 2: got %q with %d bugs, want Talos with 1", groups[2].Name, len(groups[2].Bugs))
	}
}

func TestNormalizePlatform(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Android hardware devices used in Raptor/Talos mobile runs
		{"android-hw-p6-13-0-arm7-shippable", "android-hw-p6"},
		{"android-hw-p6-13-0-arm64-shippable", "android-hw-p6"},
		{"android-hw-a55-14-0-arm7-shippable", "android-hw-a55"},
		{"android-hw-a55-14-0-arm64-shippable", "android-hw-a55"},
		// Linux platforms common in AWSY and Talos (version preserved)
		{"linux1804-64-shippable-qr", "linux1804"},
		{"linux2204-64-qr", "linux2204"},
		{"linux2404-64-shippable", "linux2404"},
		// macOS platforms used in Raptor (version preserved for intel vs apple silicon distinction)
		{"macosx1015-64-shippable-qr", "macosx1015"},
		{"macosx1470-64-shippable", "macosx1470"},
		// Windows platforms used in Talos and mozperftest
		{"windows11-64-2009-shippable", "windows11"},
		{"windows10-64-2009-shippable", "windows10"},
		// Unknown platforms returned as-is
		{"unknown-platform", "unknown-platform"},
		{"toolchains", "toolchains"},
		{"", ""},
	}

	for _, tt := range tests {
		got := normalizePlatform(tt.input)
		if got != tt.expected {
			t.Errorf("normalizePlatform(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestFetchFailureRate(t *testing.T) {
	payload := []THDailyCount{
		{TestRuns: 100, FailureCount: 10},
		{TestRuns: 200, FailureCount: 30},
		{TestRuns: 100, FailureCount: 10},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	old := treeherderBase
	treeherderBase = server.URL
	defer func() { treeherderBase = old }()

	rate := fetchFailureRate(1234, "2026-03-14", "2026-03-21")
	// 50 failures / 400 runs = 12.5%
	if rate != "12.5%" {
		t.Errorf("got %q, want %q", rate, "12.5%")
	}
}

func TestFetchFailureRateZeroRuns(t *testing.T) {
	payload := []THDailyCount{{TestRuns: 0, FailureCount: 0}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	old := treeherderBase
	treeherderBase = server.URL
	defer func() { treeherderBase = old }()

	rate := fetchFailureRate(1234, "2026-03-14", "2026-03-21")
	if rate != "" {
		t.Errorf("expected empty string for zero runs, got %q", rate)
	}
}

func TestAggregateBreakdown(t *testing.T) {
	failures := []THJobFailure{
		{Platform: "linux1804-64-shippable-qr", Tree: "autoland", TestSuite: "raptor-tp6"},
		{Platform: "linux1804-64-shippable-qr", Tree: "autoland", TestSuite: "raptor-tp6"},
		{Platform: "windows11-64-2009-shippable", Tree: "autoland", TestSuite: "talos-g5"},
		{Platform: "macosx1470-64-shippable", Tree: "mozilla-central", TestSuite: "raptor-speedometer"},
		{Platform: "toolchains", Tree: "mozilla-central", TestSuite: "toolchain-linux64-custom-car"},
	}

	breakdowns, platforms := aggregateBreakdown(failures)

	expectedBreakdowns := []string{"autoland: 3", "mozilla-central: 2"}
	if len(breakdowns) != len(expectedBreakdowns) {
		t.Fatalf("breakdowns: got %v, want %v", breakdowns, expectedBreakdowns)
	}
	for i, b := range breakdowns {
		if b != expectedBreakdowns[i] {
			t.Errorf("breakdown[%d]: got %q, want %q", i, b, expectedBreakdowns[i])
		}
	}

	// toolchain-linux64-custom-car should resolve to "linux" via fallback
	for _, want := range []string{"linux1804: 2", "macosx1470: 1", "windows11: 1", "linux: 1"} {
		found := false
		for _, got := range platforms {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("platforms: expected %q in %v", want, platforms)
		}
	}
}

func TestFetchTreeherderCounts(t *testing.T) {
	bugID1, bugID2 := 1234, 5678
	payload := []THFailure{
		{BugID: &bugID1, BugCount: 142},
		{BugID: &bugID2, BugCount: 37},
		{BugID: nil, BugCount: 99}, // unclassified, should be ignored
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	old := treeherderBase
	treeherderBase = server.URL
	defer func() { treeherderBase = old }()

	counts := fetchTreeherderCounts("2026-03-12", "2026-03-19")

	if counts[1234] != 142 {
		t.Errorf("bug 1234: got %d, want 142", counts[1234])
	}
	if counts[5678] != 37 {
		t.Errorf("bug 5678: got %d, want 37", counts[5678])
	}
	if _, ok := counts[0]; ok {
		t.Error("nil bug_id should not be present in counts map")
	}
}

func TestFetchTreeherderBreakdown(t *testing.T) {
	payload := []THJobFailure{
		{Platform: "linux1804-64-shippable-qr", Tree: "autoland", TestSuite: "raptor-tp6"},
		{Platform: "linux1804-64-shippable-qr", Tree: "autoland", TestSuite: "raptor-tp6"},
		{Platform: "android-hw-p6-13-0-arm64-shippable", Tree: "autoland", TestSuite: "raptor-speedometer"},
		{Platform: "windows11-64-2009-shippable", Tree: "mozilla-central", TestSuite: "talos-g5"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	old := treeherderBase
	treeherderBase = server.URL
	defer func() { treeherderBase = old }()

	breakdowns, platforms := fetchTreeherderBreakdown(1234, "2026-03-12", "2026-03-19")

	if len(breakdowns) != 2 {
		t.Fatalf("breakdowns: got %v, want 2 entries", breakdowns)
	}
	if breakdowns[0] != "autoland: 3" {
		t.Errorf("breakdowns[0]: got %q, want %q", breakdowns[0], "autoland: 3")
	}
	if breakdowns[1] != "mozilla-central: 1" {
		t.Errorf("breakdowns[1]: got %q, want %q", breakdowns[1], "mozilla-central: 1")
	}

	for _, want := range []string{"android-hw-p6: 1", "linux1804: 2", "windows11: 1"} {
		found := false
		for _, got := range platforms {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("platforms: expected %q in %v", want, platforms)
		}
	}
}

func TestFetchIntermittentBugs(t *testing.T) {
	payload := BugListResponse{Bugs: []Bug{
		{ID: 1, Summary: "Intermittent raptor test failure"},
		{ID: 2, Summary: "Perma talos regression"}, // should be filtered out
		{ID: 3, Summary: "Intermittent AWSY failure"},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	old := bugzillaBase
	bugzillaBase = server.URL
	defer func() { bugzillaBase = old }()

	bugs := fetchIntermittentBugs()

	if len(bugs) != 2 {
		t.Fatalf("got %d bugs, want 2 (perma should be filtered)", len(bugs))
	}
	for _, b := range bugs {
		if b.ID == 2 {
			t.Error("perma bug should have been filtered out")
		}
	}
}

func TestAnalyzeAllFiltersAndSorts(t *testing.T) {
	maxConcurrent = 5
	threshold = 20
	breakdownPayload := []THJobFailure{
		{Platform: "linux1804-64-shippable-qr", Tree: "autoland", TestSuite: "raptor-tp6"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(breakdownPayload); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	old := treeherderBase
	treeherderBase = server.URL
	defer func() { treeherderBase = old }()

	bugs := []Bug{
		{ID: 100, Summary: "Intermittent raptor failure"},
		{ID: 200, Summary: "Intermittent talos failure"},
		{ID: 300, Summary: "Intermittent AWSY failure"},
	}

	counts := map[int]int{100: 50, 200: 10, 300: 100}
	prevCounts := map[int]int{100: 30, 300: 60}
	results := analyzeAll(bugs, "2026-03-12", "2026-03-19", counts, prevCounts, "2026-03-17", map[int]int{}, map[int]int{})

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (bug 200 below threshold)", len(results))
	}
	if results[0].ID != 300 || results[0].NumberFailures != 100 {
		t.Errorf("first result should be bug 300 with 100 failures, got bug %d with %d", results[0].ID, results[0].NumberFailures)
	}
	if results[1].ID != 100 || results[1].NumberFailures != 50 {
		t.Errorf("second result should be bug 100 with 50 failures, got bug %d with %d", results[1].ID, results[1].NumberFailures)
	}
	if results[0].Trend != "↑ +40" {
		t.Errorf("bug 300 trend: got %q, want %q", results[0].Trend, "↑ +40")
	}
	if results[1].Trend != "↑ +20" {
		t.Errorf("bug 100 trend: got %q, want %q", results[1].Trend, "↑ +20")
	}
}

func TestRenderHTML(t *testing.T) {
	results := []Result{
		{ID: 1234, Summary: "Intermittent raptor timeout", Component: "Raptor", NumberFailures: 42,
			Link: "https://bugzilla.mozilla.org/show_bug.cgi?id=1234", GraphLink: "https://treeherder.mozilla.org/",
			Platforms: []string{"linux1804: 3"}, BreakdownList: []string{"autoland: 3"}},
	}
	permas := []PermaBug{
		{ID: 5678, Summary: "Perma talos failure", Component: "Talos",
			Link: "https://bugzilla.mozilla.org/show_bug.cgi?id=5678", GraphLink: "https://treeherder.mozilla.org/"},
	}

	writeHTMLReport(results, permas)

	// Use renderHTML directly with a buffer to verify output
	var buf bytes.Buffer
	data := reportData{
		Intermittents: groupByComponent(results, components),
		Permas:        groupByComponent(permas, components),
		Generated:     "2026-03-19 09:00 UTC",
		DaysBack:      7,
	}

	tmpl := reportTemplate
	if err := renderHTML(&buf, tmpl, data); err != nil {
		t.Fatalf("renderHTML failed: %v", err)
	}

	html := buf.String()
	for _, want := range []string{
		"Bug 1234", "Intermittent raptor timeout", "Raptor",
		"42", "linux1804: 3", "autoland: 3",
		"Bug 5678", "Perma talos failure", "Talos",
		"PerfTest Triage Report",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("expected %q in HTML output", want)
		}
	}
}

func TestFetchPermaBugs(t *testing.T) {
	payload := BugListResponse{Bugs: []Bug{
		{ID: 10, Summary: "Perma raptor-browsertime timeout", Component: "Raptor",
			AssignedTo: "dev@mozilla.com",
			Flags: []struct {
				Name      string `json:"name"`
				Requestee string `json:"requestee"`
			}{{Name: "needinfo", Requestee: "manager@mozilla.com"}}},
		{ID: 11, Summary: "Perma talos regression", Component: "Talos",
			AssignedTo: "nobody@mozilla.org"},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	old := bugzillaBase
	bugzillaBase = server.URL
	defer func() { bugzillaBase = old }()

	bugs := fetchPermaBugs("2026-03-12", "2026-03-19")

	if len(bugs) != 2 {
		t.Fatalf("got %d bugs, want 2", len(bugs))
	}

	if bugs[0].Assignee != "dev@mozilla.com" {
		t.Errorf("assignee: got %q, want %q", bugs[0].Assignee, "dev@mozilla.com")
	}
	if bugs[0].Needinfo != "manager@mozilla.com" {
		t.Errorf("needinfo: got %q, want %q", bugs[0].Needinfo, "manager@mozilla.com")
	}
	if bugs[1].Assignee != "" {
		t.Errorf("nobody@mozilla.org should be treated as unassigned, got %q", bugs[1].Assignee)
	}
	if bugs[0].GraphLink == "" {
		t.Error("GraphLink should be set")
	}
	if bugs[0].Component != "Raptor" {
		t.Errorf("component: got %q, want Raptor", bugs[0].Component)
	}
}

func TestEnrichPermas(t *testing.T) {
	breakdownPayload := []THJobFailure{
		{Platform: "linux1804-64-shippable-qr", Tree: "autoland", TestSuite: "raptor-tp6"},
		{Platform: "windows11-64-2009-shippable", Tree: "mozilla-central", TestSuite: "talos-g5"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(breakdownPayload); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	old := treeherderBase
	treeherderBase = server.URL
	defer func() { treeherderBase = old }()

	permas := []PermaBug{
		{ID: 10, Summary: "Perma raptor timeout", Component: "Raptor"},
		{ID: 11, Summary: "Perma talos regression", Component: "Talos"},
	}

	enriched := enrichPermas(permas, "2026-03-12", "2026-03-19", "2026-03-17", map[int]int{10: 50, 11: 30}, map[int]int{10: 20, 11: 10})

	for _, p := range enriched {
		if len(p.BreakdownList) == 0 {
			t.Errorf("bug %d: expected breakdown to be populated", p.ID)
		}
		if len(p.Platforms) == 0 {
			t.Errorf("bug %d: expected platforms to be populated", p.ID)
		}
	}
}

func TestGetRetry(t *testing.T) {
	retrySleep = func(time.Duration) {}
	defer func() { retrySleep = func(d time.Duration) { time.Sleep(d) } }()
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`"ok"`)); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer server.Close()

	resp, err := get(server.URL)
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts.Load())
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestGetRetryExhausted(t *testing.T) {
	retrySleep = func(time.Duration) {}
	defer func() { retrySleep = func(d time.Duration) { time.Sleep(d) } }()
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	resp, err := get(server.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts.Load())
	}
}
