package main

import (
	"testing"
)

func TestStartedSuffix(t *testing.T) {
	t.Parallel()

	if startedSuffix(true) != " and started" {
		t.Fatal("startedSuffix(true) wrong")
	}

	if startedSuffix(false) == "" {
		t.Fatal("startedSuffix(false) should explain the boot behavior")
	}
}
