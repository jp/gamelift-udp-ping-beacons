package main

import (
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
		wantErr  bool
	}{
		{"valid", time.Second, time.Millisecond, time.Second, "auto", "table", false},
		{"bad duration", 0, time.Millisecond, time.Second, "auto", "table", true},
		{"bad family", time.Second, time.Millisecond, time.Second, "ipx", "table", true},
		{"bad format", time.Second, time.Millisecond, time.Second, "auto", "xml", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFlags(tt.duration, tt.interval, tt.timeout, tt.family, tt.format)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateFlags() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
