package main

import "testing"

func TestNormalizeErrorKind(t *testing.T) {
	for raw, want := range map[string]string{
		"budget": "budget", "prompt_too_long": "prompt_too_long", "timeout": "timeout",
		"auth": "auth", "rate": "rate_limit", "retry": "rate_limit", "input": "content",
		"dead": "content", "provider": "provider", "": "internal", "weird": "internal",
	} {
		if got := normalizeErrorKind(raw); got != want {
			t.Errorf("normalizeErrorKind(%q)=%s，期望 %s", raw, got, want)
		}
	}
}
