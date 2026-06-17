package main

import (
	"context"
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
		{"bad duration", 0, time.Millisecond, time.Second, "auto", "table", "auto", true},
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
	}}, time.Second, time.Millisecond, time.Millisecond, "ipv6", progress)

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Error == "" {
		t.Fatal("expected endpoint error")
	}

	select {
	case event := <-progress:
		if event.Done != 1 {
			t.Fatalf("event.Done = %d, want 1", event.Done)
		}
	default:
		t.Fatal("expected progress event")
	}
}
