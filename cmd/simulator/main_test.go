package main

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "milliseconds", value: "250ms", want: 250 * time.Millisecond},
		{name: "seconds", value: "1.5s", want: 1500 * time.Millisecond},
		{name: "zero", value: "0", want: 0},
		{name: "empty", value: "", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseDuration(tt.value); got != tt.want {
				t.Fatalf("parseDuration(%q) = %s, want %s", tt.value, got, tt.want)
			}
		})
	}
}
