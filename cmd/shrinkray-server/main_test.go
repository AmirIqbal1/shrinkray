package main

import "testing"

func TestRootValuesAreRepeatable(t *testing.T) {
	var roots rootValues
	if err := roots.Set("/media/movies"); err != nil {
		t.Fatal(err)
	}
	if err := roots.Set("TV=/media/tv"); err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 || roots[0] != "/media/movies" || roots[1] != "TV=/media/tv" {
		t.Fatalf("repeatable root values = %#v", roots)
	}
}
