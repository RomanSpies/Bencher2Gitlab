package main

import (
	"errors"
	"flag"
	"testing"

	"github.com/RomanSpies/Bencher2Gitlab/internal/app"
)

func TestParseArgsVersion(t *testing.T) {
	if _, err := parseArgs([]string{"--version"}); !errors.Is(err, errVersion) {
		t.Fatalf("err = %v, want errVersion", err)
	}
}

func TestParseArgsThread(t *testing.T) {
	cfg, err := parseArgs([]string{"--thread", "--on-zero-alerts", "auto"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if !cfg.Thread || cfg.OnZeroAlerts != app.ZeroAuto {
		t.Errorf("cfg = %+v, want Thread and auto mode", cfg)
	}
}

func TestParseArgsDefaultsToNoteMode(t *testing.T) {
	cfg, err := parseArgs(nil)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if cfg.Thread {
		t.Error("thread mode must be opt-in")
	}
}

func TestParseArgsRejectsInvalidInput(t *testing.T) {
	tests := [][]string{
		{"--on-zero-alerts", "never"},
		{"stray"},
	}
	for _, args := range tests {
		if _, err := parseArgs(args); err == nil || errors.Is(err, flag.ErrHelp) {
			t.Errorf("parseArgs(%q) err = %v, want a usage error", args, err)
		}
	}
}
