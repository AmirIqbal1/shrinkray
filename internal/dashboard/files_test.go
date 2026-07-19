package dashboard

import (
	"os"
	"path/filepath"
	"testing"
)

func makeTestRoot(t *testing.T) (*SafeRoot, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := NewSafeRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root, dir
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("movie"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAllowedPathInsideRoot(t *testing.T) {
	root, dir := makeTestRoot(t)
	if err := os.Mkdir(filepath.Join(dir, "classics"), 0o700); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "classics", "movie.mkv")
	writeTestFile(t, want)
	got, relative, err := root.ResolveVideo("classics/movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if got != want || relative != "classics/movie.mkv" {
		t.Fatalf("ResolveVideo() = %q, %q; want %q, classics/movie.mkv", got, relative, want)
	}
}

func TestRejectsDotDot(t *testing.T) {
	root, _ := makeTestRoot(t)
	if _, _, err := root.ResolveVideo("../movie.mkv"); err == nil {
		t.Fatal("ResolveVideo accepted ..")
	}
}

func TestRejectsAbsolutePathOutsideRoot(t *testing.T) {
	root, _ := makeTestRoot(t)
	outside := filepath.Join(t.TempDir(), "movie.mkv")
	writeTestFile(t, outside)
	if _, _, err := root.ResolveVideo(outside); err == nil {
		t.Fatal("ResolveVideo accepted an absolute path")
	}
}

func TestRejectsSymlinkEscape(t *testing.T) {
	root, dir := makeTestRoot(t)
	outside := filepath.Join(t.TempDir(), "movie.mkv")
	writeTestFile(t, outside)
	if err := os.Symlink(outside, filepath.Join(dir, "escape.mkv")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := root.ResolveVideo("escape.mkv"); err == nil {
		t.Fatal("ResolveVideo accepted a symlink escape")
	}
}

func TestSupportedVideoExtensions(t *testing.T) {
	tests := map[string]bool{
		"movie.mkv": true, "MOVIE.MP4": true, "clip.webm": true,
		"notes.txt": false, "no-extension": false, "movie.mkv.exe": false,
	}
	for name, want := range tests {
		if got := SupportedVideo(name); got != want {
			t.Errorf("SupportedVideo(%q) = %v; want %v", name, got, want)
		}
	}
}

func TestListFoldersFirstAndHidesUnsupportedFiles(t *testing.T) {
	root, dir := makeTestRoot(t)
	if err := os.Mkdir(filepath.Join(dir, "Folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "movie.mkv"))
	writeTestFile(t, filepath.Join(dir, "notes.txt"))
	writeTestFile(t, filepath.Join(dir, "old.shrunk.mkv"))
	list, err := root.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Entries) != 2 || list.Entries[0].Type != "directory" || list.Entries[1].Name != "movie.mkv" {
		t.Fatalf("unexpected entries: %#v", list.Entries)
	}
}
