package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var ErrInvalidPath = errors.New("invalid movie path")

var videoExtensions = map[string]struct{}{
	".mkv": {}, ".mp4": {}, ".m4v": {}, ".avi": {}, ".mov": {},
	".wmv": {}, ".flv": {}, ".webm": {}, ".ts": {},
}

type SafeRoot struct {
	path string
}

type FileEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size,omitempty"`
}

type FileList struct {
	Path    string      `json:"path"`
	Parent  *string     `json:"parent"`
	Entries []FileEntry `json:"entries"`
}

type MovieDetails struct {
	RootID         string  `json:"root_id"`
	RootLabel      string  `json:"root_label"`
	Filename       string  `json:"filename"`
	Path           string  `json:"path"`
	Size           int64   `json:"size"`
	Duration       float64 `json:"duration_seconds"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	VideoCodec     string  `json:"video_codec"`
	AudioTracks    int     `json:"audio_tracks"`
	SubtitleTracks int     `json:"subtitle_tracks"`
}

func NewSafeRoot(root string) (*SafeRoot, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve movie root: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve movie root: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, fmt.Errorf("inspect movie root: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("movie root is not a directory")
	}
	return &SafeRoot{path: filepath.Clean(real)}, nil
}

func (r *SafeRoot) Path() string { return r.path }

func SupportedVideo(path string) bool {
	_, ok := videoExtensions[strings.ToLower(filepath.Ext(path))]
	return ok
}

func containsDotDot(path string) bool {
	for _, part := range strings.FieldsFunc(filepath.ToSlash(path), func(r rune) bool { return r == '/' }) {
		if part == ".." {
			return true
		}
	}
	return false
}

func (r *SafeRoot) resolve(relative string) (string, string, error) {
	if filepath.IsAbs(relative) || containsDotDot(relative) {
		return "", "", ErrInvalidPath
	}
	clean := filepath.Clean(relative)
	if clean == "." {
		clean = ""
	}
	joined := filepath.Join(r.path, clean)
	real, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", "", ErrInvalidPath
	}
	rel, err := filepath.Rel(r.path, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", "", ErrInvalidPath
	}
	return real, filepath.ToSlash(clean), nil
}

func (r *SafeRoot) ResolveDir(relative string) (string, string, error) {
	abs, clean, err := r.resolve(relative)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", "", ErrInvalidPath
	}
	return abs, clean, nil
}

func (r *SafeRoot) ResolveVideo(relative string) (string, string, error) {
	if strings.Contains(strings.ToLower(filepath.Base(relative)), ".shrunk.") || !SupportedVideo(relative) {
		return "", "", ErrInvalidPath
	}
	abs, clean, err := r.resolve(relative)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", ErrInvalidPath
	}
	return abs, clean, nil
}

func (r *SafeRoot) List(relative string) (FileList, error) {
	abs, clean, err := r.ResolveDir(relative)
	if err != nil {
		return FileList{}, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return FileList{}, err
	}
	result := FileList{Path: clean, Entries: make([]FileEntry, 0)}
	if clean != "" {
		parent := filepath.ToSlash(filepath.Dir(clean))
		if parent == "." {
			parent = ""
		}
		result.Parent = &parent
	}
	for _, entry := range entries {
		rel := filepath.ToSlash(filepath.Join(clean, entry.Name()))
		resolved, _, resolveErr := r.resolve(rel)
		if resolveErr != nil {
			continue
		}
		info, statErr := os.Stat(resolved)
		if statErr != nil {
			continue
		}
		if info.IsDir() {
			result.Entries = append(result.Entries, FileEntry{Name: entry.Name(), Path: rel, Type: "directory"})
		} else if info.Mode().IsRegular() && SupportedVideo(entry.Name()) && !strings.Contains(strings.ToLower(entry.Name()), ".shrunk.") {
			result.Entries = append(result.Entries, FileEntry{Name: entry.Name(), Path: rel, Type: "file", Size: info.Size()})
		}
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		if result.Entries[i].Type != result.Entries[j].Type {
			return result.Entries[i].Type == "directory"
		}
		return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
	})
	return result, nil
}

type ffprobeResult struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

func (r *SafeRoot) Probe(relative string) (MovieDetails, error) {
	abs, clean, err := r.ResolveVideo(relative)
	if err != nil {
		return MovieDetails{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries",
		"format=duration:stream=codec_type,codec_name,width,height", "-of", "json", abs).Output()
	if err != nil {
		return MovieDetails{}, errors.New("ffprobe could not inspect this movie")
	}
	var probe ffprobeResult
	if err := json.Unmarshal(output, &probe); err != nil {
		return MovieDetails{}, errors.New("ffprobe returned invalid movie details")
	}
	info, err := os.Stat(abs)
	if err != nil {
		return MovieDetails{}, ErrInvalidPath
	}
	details := MovieDetails{Filename: filepath.Base(clean), Path: clean, Size: info.Size()}
	if _, err := fmt.Sscan(probe.Format.Duration, &details.Duration); err != nil {
		return MovieDetails{}, errors.New("ffprobe did not report a valid duration")
	}
	for _, stream := range probe.Streams {
		switch stream.CodecType {
		case "video":
			if details.VideoCodec == "" {
				details.VideoCodec, details.Width, details.Height = stream.CodecName, stream.Width, stream.Height
			}
		case "audio":
			details.AudioTracks++
		case "subtitle":
			details.SubtitleTracks++
		}
	}
	if details.VideoCodec == "" {
		return MovieDetails{}, errors.New("no video stream was found")
	}
	return details, nil
}

func outputPath(source, container string) string {
	ext := filepath.Ext(source)
	return strings.TrimSuffix(source, ext) + ".shrunk." + container
}
