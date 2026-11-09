package main

import (
	"math"
	"testing"
)

func TestDesiredRunnerTargetBounds(t *testing.T) {
	tests := []struct {
		name                                string
		minIdle, maxRunners, assigned, want int
	}{
		{"disabled", 0, 0, 100, 0},
		{"default zero idle", 0, 4, 0, 0},
		{"one clean warm slot", 1, 4, 0, 1},
		{"warm plus demand", 1, 4, 2, 3},
		{"hard cap", 1, 4, 100, 4},
		{"overflow must cap", 1, 4, math.MaxInt, 4},
		{"negative assigned preserves warm floor", 1, 4, -100, 1},
		{"negative warm floor", -2, 4, 0, 0},
		{"warm floor capped", 10, 4, 0, 4},
		{"negative maximum disabled", 1, -1, 10, 0},
		{"max integer without overflow", 1, math.MaxInt, math.MaxInt, math.MaxInt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := desiredRunnerTarget(tt.minIdle, tt.maxRunners, tt.assigned); got != tt.want {
				t.Fatalf("desiredRunnerTarget(%d, %d, %d) = %d; want %d", tt.minIdle, tt.maxRunners, tt.assigned, got, tt.want)
			}
		})
	}
}
