package main

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestFinalizeResult(t *testing.T) {
	res := result{
		Sent:     5,
		Received: 3,
		Samples: []time.Duration{
			10 * time.Millisecond,
			20 * time.Millisecond,
			15 * time.Millisecond,
		},
	}

	finalizeResult(&res)

	if res.Lost != 2 {
		t.Fatalf("lost = %d, want 2", res.Lost)
	}
	if res.Loss != 40 {
		t.Fatalf("loss = %f, want 40", res.Loss)
	}
	if res.Min != 10*time.Millisecond {
		t.Fatalf("min = %s, want 10ms", res.Min)
	}
	if res.Avg != 15*time.Millisecond {
		t.Fatalf("avg = %s, want 15ms", res.Avg)
	}
	if res.Max != 20*time.Millisecond {
		t.Fatalf("max = %s, want 20ms", res.Max)
	}
	if res.P50 != 15*time.Millisecond {
		t.Fatalf("p50 = %s, want 15ms", res.P50)
	}
	if res.Jitter != 7500*time.Microsecond {
		t.Fatalf("jitter = %s, want 7.5ms", res.Jitter)
	}
}

func TestValidateFlags(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		interval time.Duration
		timeout  time.Duration
		family   string
		format   string
		pause    string
		wantErr  bool
	}{
		{"valid", time.Second, time.Millisecond, time.Second, "auto", "table", "auto", false},
		{"continuous duration", 0, time.Millisecond, time.Second, "auto", "table", "auto", false},
		{"bad duration", -time.Second, time.Millisecond, time.Second, "auto", "table", "auto", true},
		{"bad family", time.Second, time.Millisecond, time.Second, "ipx", "table", "auto", true},
		{"bad format", time.Second, time.Millisecond, time.Second, "auto", "xml", "auto", true},
		{"bad pause", time.Second, time.Millisecond, time.Second, "auto", "table", "sometimes", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFlags(tt.duration, tt.interval, tt.timeout, tt.family, tt.format, tt.pause)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateFlags() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestShouldPause(t *testing.T) {
	tests := []struct {
		name                string
		pause               string
		goos                string
		launchedWithoutArgs bool
		stdinIsTerminal     bool
		want                bool
	}{
		{"windows double click", "auto", "windows", true, true, true},
		{"windows with args", "auto", "windows", false, true, false},
		{"non windows auto", "auto", "darwin", true, true, false},
		{"always terminal", "always", "darwin", false, true, true},
		{"always redirected", "always", "windows", true, false, false},
		{"never", "never", "windows", true, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldPause(tt.pause, tt.goos, tt.launchedWithoutArgs, tt.stdinIsTerminal)
			if got != tt.want {
				t.Fatalf("shouldPause() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunAllSendsProgress(t *testing.T) {
	progress := make(chan progressEvent, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	results := runAll(ctx, []endpoint{{
		Continent:   "Test",
		Name:        "No IPv6",
		Code:        "test-1",
		Address:     "example.invalid:7770",
		Protocol:    "UDP",
		IPv6Support: false,
	}}, time.Second, time.Millisecond, time.Millisecond, "ipv6", progress, nil)

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Error == "" {
		t.Fatal("expected endpoint error")
	}

	select {
	case event := <-progress:
		if event.Index != 0 {
			t.Fatalf("event.Index = %d, want 0", event.Index)
		}
		if !event.Done {
			t.Fatal("event.Done = false, want true")
		}
		if event.Result.Endpoint.Code != "test-1" {
			t.Fatalf("event.Result.Endpoint.Code = %q, want test-1", event.Result.Endpoint.Code)
		}
	default:
		t.Fatal("expected progress event")
	}
}

func TestSummarizeResults(t *testing.T) {
	summary := summarizeResults([]result{
		{Received: 10},
		{Received: 10, Loss: 5},
		{Received: 0},
		{Received: 1, Error: "lookup failed"},
	})

	if summary.ok != 1 {
		t.Fatalf("summary.ok = %d, want 1", summary.ok)
	}
	if summary.degraded != 1 {
		t.Fatalf("summary.degraded = %d, want 1", summary.degraded)
	}
	if summary.down != 2 {
		t.Fatalf("summary.down = %d, want 2", summary.down)
	}
}

func TestResultStatus(t *testing.T) {
	tests := []struct {
		name string
		res  result
		want string
	}{
		{"waiting", result{}, "waiting"},
		{"error", result{Error: "lookup failed"}, "error"},
		{"no replies", result{Sent: 2}, "no replies"},
		{"packet loss", result{Sent: 2, Received: 1, Loss: 50}, "packet loss"},
		{"ok", result{Sent: 2, Received: 2}, "ok"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resultStatus(tt.res)
			if got != tt.want {
				t.Fatalf("resultStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPauseStateToggle(t *testing.T) {
	var pause pauseState
	if pause.isPaused() {
		t.Fatal("new pauseState is paused")
	}
	if paused := pause.toggle(); !paused {
		t.Fatal("toggle() = false, want true")
	}
	if !pause.isPaused() {
		t.Fatal("pauseState should be paused")
	}
	if paused := pause.toggle(); paused {
		t.Fatal("toggle() = true, want false")
	}
}

func TestDurationLabel(t *testing.T) {
	if got := durationLabel(0); got != "continuous" {
		t.Fatalf("durationLabel(0) = %q, want continuous", got)
	}
	if got := durationLabel(10 * time.Second); got != "duration 10s" {
		t.Fatalf("durationLabel(10s) = %q, want duration 10s", got)
	}
}

func TestSortedResultsByAvgLatency(t *testing.T) {
	results := []result{
		{Endpoint: endpoint{Code: "waiting"}},
		{Endpoint: endpoint{Code: "slow"}, Received: 2, Avg: 50 * time.Millisecond},
		{Endpoint: endpoint{Code: "error"}, Error: "lookup failed"},
		{Endpoint: endpoint{Code: "fast"}, Received: 2, Avg: 10 * time.Millisecond},
		{Endpoint: endpoint{Code: "middle"}, Received: 2, Avg: 25 * time.Millisecond},
	}

	sorted := sortedResultsByAvgLatency(results)
	got := []string{
		sorted[0].Endpoint.Code,
		sorted[1].Endpoint.Code,
		sorted[2].Endpoint.Code,
		sorted[3].Endpoint.Code,
		sorted[4].Endpoint.Code,
	}
	want := []string{"fast", "middle", "slow", "error", "waiting"}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted codes = %v, want %v", got, want)
		}
	}

	if results[0].Endpoint.Code != "waiting" {
		t.Fatal("sortedResultsByAvgLatency mutated input slice")
	}
}

func TestTableHeightForTerminal(t *testing.T) {
	if got := tableHeightForTerminal(50, false); got != 45 {
		t.Fatalf("tableHeightForTerminal(50, false) = %d, want 45", got)
	}
	if got := tableHeightForTerminal(50, true); got != 42 {
		t.Fatalf("tableHeightForTerminal(50, true) = %d, want 42", got)
	}
	if got := tableHeightForTerminal(10, false); got != 8 {
		t.Fatalf("tableHeightForTerminal(10, false) = %d, want 8", got)
	}
}

func TestFormatTraceOutput(t *testing.T) {
	if got := formatTraceOutput("hop 1\n", nil); got != "hop 1" {
		t.Fatalf("formatTraceOutput() = %q, want hop 1", got)
	}
	if got := formatTraceOutput("", nil); got != "(no traceroute output)" {
		t.Fatalf("empty formatTraceOutput() = %q", got)
	}
	if got := formatTraceOutput("hop 1", context.DeadlineExceeded); !strings.Contains(got, "traceroute error") {
		t.Fatalf("error formatTraceOutput() = %q, want traceroute error", got)
	}
	if got := formatTraceOutput("", os.ErrPermission); !strings.Contains(got, "ICMP/raw socket permission") {
		t.Fatalf("permission formatTraceOutput() = %q, want permission explanation", got)
	}
}

func TestRollingAvg(t *testing.T) {
	if got := rollingAvg(0, 10*time.Millisecond, 1); got != 10*time.Millisecond {
		t.Fatalf("rollingAvg first = %s, want 10ms", got)
	}
	if got := rollingAvg(10*time.Millisecond, 20*time.Millisecond, 2); got != 15*time.Millisecond {
		t.Fatalf("rollingAvg second = %s, want 15ms", got)
	}
}

func TestFormatTraceHops(t *testing.T) {
	output := formatTraceHops("example.com", net.ParseIP("203.0.113.1"), []traceHop{{
		TTL:      1,
		IP:       net.ParseIP("192.0.2.1"),
		Host:     "router.example",
		Sent:     2,
		Received: 1,
		LastRTT:  10 * time.Millisecond,
		AvgRTT:   10 * time.Millisecond,
	}}, true)
	if !strings.Contains(output, "router.example") {
		t.Fatalf("formatTraceHops() missing reverse DNS: %q", output)
	}
	if !strings.Contains(output, "50.0%") {
		t.Fatalf("formatTraceHops() missing loss: %q", output)
	}
}
