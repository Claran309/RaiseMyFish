package main

import "testing"

func TestCheckBenchDatabase(t *testing.T) {
	if err := checkBenchDatabase("raisemyfish_bench"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"raisemyfish", "postgres", ""} {
		if err := checkBenchDatabase(name); err == nil {
			t.Fatalf("非 bench 库被接受：%q", name)
		}
	}
}
