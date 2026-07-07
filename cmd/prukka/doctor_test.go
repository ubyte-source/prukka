package main

import (
	"testing"

	"github.com/ubyte-source/prukka/internal/doctor"
)

func TestCountFailed(t *testing.T) {
	t.Parallel()

	checks := []doctor.Check{
		{Name: "a", Status: doctor.StatusOK},
		{Name: "b", Status: doctor.StatusFail},
		{Name: "c", Status: doctor.StatusWarn},
		{Name: "d", Status: doctor.StatusFail},
	}

	if got := countFailed(checks); got != 2 {
		t.Fatalf("countFailed = %d, want 2", got)
	}
}
