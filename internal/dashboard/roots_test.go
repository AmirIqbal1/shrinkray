package dashboard

import (
	"os"
	"path/filepath"
	"testing"
)

func makeRootDirectories(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	movies := filepath.Join(base, "movies")
	tv := filepath.Join(base, "tv")
	for _, directory := range []string{movies, tv} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return movies, tv
}

func TestRootRegistrySupportsOneRoot(t *testing.T) {
	movies, _ := makeRootDirectories(t)
	registry, err := NewRootRegistry([]string{movies})
	if err != nil {
		t.Fatal(err)
	}
	if roots := registry.Summaries(); len(roots) != 1 || roots[0].ID != "movies" || roots[0].Label != "Movies" {
		t.Fatalf("root summaries = %#v", roots)
	}
}

func TestRootRegistryRequiresAtLeastOneAbsoluteDirectory(t *testing.T) {
	if _, err := NewRootRegistry(nil); err == nil {
		t.Fatal("NewRootRegistry accepted no roots")
	}
	if _, err := NewRootRegistry([]string{"relative/path"}); err == nil {
		t.Fatal("NewRootRegistry accepted a relative root")
	}
	if _, err := NewRootRegistry([]string{filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("NewRootRegistry accepted a nonexistent root")
	}
	file := filepath.Join(t.TempDir(), "not-a-directory")
	writeTestFile(t, file)
	if _, err := NewRootRegistry([]string{file}); err == nil {
		t.Fatal("NewRootRegistry accepted a non-directory root")
	}
}

func TestRootRegistryParsesTwoAutomaticRootsInOrder(t *testing.T) {
	movies, tv := makeRootDirectories(t)
	registry, err := NewRootRegistry([]string{movies, tv})
	if err != nil {
		t.Fatal(err)
	}
	want := []RootSummary{{ID: "movies", Label: "Movies"}, {ID: "tv", Label: "TV"}}
	got := registry.Summaries()
	if len(got) != len(want) {
		t.Fatalf("root summaries = %#v; want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("root summaries = %#v; want %#v", got, want)
		}
	}
}

func TestRootRegistryParsesExplicitLabels(t *testing.T) {
	movies, tv := makeRootDirectories(t)
	registry, err := NewRootRegistry([]string{"Films & Cinema=" + movies, "TV Shows=" + tv})
	if err != nil {
		t.Fatal(err)
	}
	got := registry.Summaries()
	if got[0] != (RootSummary{ID: "films-cinema", Label: "Films & Cinema"}) || got[1] != (RootSummary{ID: "tv-shows", Label: "TV Shows"}) {
		t.Fatalf("explicit root summaries = %#v", got)
	}
}

func TestRootRegistryRejectsDuplicateCanonicalPaths(t *testing.T) {
	movies, _ := makeRootDirectories(t)
	alias := filepath.Join(t.TempDir(), "movies-alias")
	if err := os.Symlink(movies, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRootRegistry([]string{"Movies=" + movies, "Films=" + alias}); err == nil {
		t.Fatal("NewRootRegistry accepted duplicate canonical paths")
	}
}

func TestRootRegistryRejectsDuplicateIDs(t *testing.T) {
	movies, tv := makeRootDirectories(t)
	if _, err := NewRootRegistry([]string{"TV Shows=" + movies, "tv-shows=" + tv}); err == nil {
		t.Fatal("NewRootRegistry accepted duplicate generated IDs")
	}
}

func TestRootRegistryRejectsOverlappingRoots(t *testing.T) {
	base := t.TempDir()
	child := filepath.Join(base, "movies")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRootRegistry([]string{"Media=" + base, "Movies=" + child}); err == nil {
		t.Fatal("NewRootRegistry accepted overlapping roots")
	}
}

func TestRootRegistryRejectsUnknownID(t *testing.T) {
	movies, _ := makeRootDirectories(t)
	registry, err := NewRootRegistry([]string{movies})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Get("unknown"); err == nil {
		t.Fatal("Get accepted an unknown root ID")
	}
}

func TestEachRootRejectsSymlinkEscape(t *testing.T) {
	movies, tv := makeRootDirectories(t)
	registry, err := NewRootRegistry([]string{movies, tv})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.mkv")
	writeTestFile(t, outside)
	for _, root := range registry.Roots() {
		if err := os.Symlink(outside, filepath.Join(root.Root.Path(), "escape.mkv")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := root.Root.ResolveVideo("escape.mkv"); err == nil {
			t.Errorf("root %q accepted a symlink escape", root.ID)
		}
	}
}

func TestRootRegistryRedactsEveryCanonicalRoot(t *testing.T) {
	movies, tv := makeRootDirectories(t)
	registry, err := NewRootRegistry([]string{"Movies=" + movies, "TV=" + tv})
	if err != nil {
		t.Fatal(err)
	}
	redacted := registry.Redact(movies + "/film.mkv then " + tv + "/show.mkv")
	if redacted != "[Movies]/film.mkv then [TV]/show.mkv" {
		t.Fatalf("Redact() = %q", redacted)
	}
}
