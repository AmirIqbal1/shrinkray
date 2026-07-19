package dashboard

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

var ErrUnknownRoot = errors.New("unknown media root")

type MediaRoot struct {
	ID    string
	Label string
	Root  *SafeRoot
}

type RootSummary struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type RootRegistry struct {
	ordered []*MediaRoot
	byID    map[string]*MediaRoot
}

func NewRootRegistry(specs []string) (*RootRegistry, error) {
	if len(specs) == 0 {
		return nil, errors.New("at least one --root is required")
	}
	registry := &RootRegistry{ordered: make([]*MediaRoot, 0, len(specs)), byID: make(map[string]*MediaRoot, len(specs))}
	canonicalPaths := make(map[string]bool, len(specs))
	for _, spec := range specs {
		label, rootPath, err := parseRootSpec(spec)
		if err != nil {
			return nil, err
		}
		root, err := NewSafeRoot(rootPath)
		if err != nil {
			return nil, fmt.Errorf("media root %q: %w", label, err)
		}
		if canonicalPaths[root.Path()] {
			return nil, fmt.Errorf("duplicate media root path for %q", label)
		}
		id := rootID(label)
		if _, exists := registry.byID[id]; exists {
			return nil, fmt.Errorf("duplicate media root ID %q", id)
		}
		for _, existing := range registry.ordered {
			if pathsOverlap(existing.Root.Path(), root.Path()) {
				return nil, fmt.Errorf("media roots %q and %q overlap", existing.Label, label)
			}
		}
		mediaRoot := &MediaRoot{ID: id, Label: label, Root: root}
		registry.ordered = append(registry.ordered, mediaRoot)
		registry.byID[id] = mediaRoot
		canonicalPaths[root.Path()] = true
	}
	return registry, nil
}

func parseRootSpec(spec string) (string, string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", errors.New("media root must not be empty")
	}
	label, rootPath := "", spec
	explicitLabel := false
	if !filepath.IsAbs(spec) {
		if separator := strings.IndexByte(spec, '='); separator >= 0 {
			explicitLabel = true
			label = sanitizeRootLabel(spec[:separator])
			rootPath = strings.TrimSpace(spec[separator+1:])
		}
	}
	if !filepath.IsAbs(rootPath) {
		return "", "", errors.New("media root path must be absolute")
	}
	if explicitLabel && label == "" {
		return "", "", errors.New("media root display name must not be empty")
	}
	if label == "" {
		label = automaticRootLabel(filepath.Base(filepath.Clean(rootPath)))
	}
	label = sanitizeRootLabel(label)
	if label == "" {
		return "", "", errors.New("media root display name must not be empty")
	}
	return label, rootPath, nil
}

func sanitizeRootLabel(label string) string {
	label = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, label)
	return strings.Join(strings.Fields(label), " ")
}

func automaticRootLabel(base string) string {
	words := strings.FieldsFunc(base, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for i, word := range words {
		runes := []rune(word)
		if len(runes) <= 3 {
			words[i] = strings.ToUpper(word)
			continue
		}
		runes[0] = unicode.ToUpper(runes[0])
		words[i] = string(runes)
	}
	if len(words) == 0 {
		return "Root"
	}
	return strings.Join(words, " ")
}

func rootID(label string) string {
	var id strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(label) {
		isASCIIAlphaNumeric := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if isASCIIAlphaNumeric {
			id.WriteRune(r)
			lastDash = false
		} else if id.Len() > 0 && !lastDash {
			id.WriteByte('-')
			lastDash = true
		}
	}
	value := strings.Trim(id.String(), "-")
	if value == "" {
		digest := sha256.Sum256([]byte(label))
		value = fmt.Sprintf("root-%x", digest[:4])
	}
	return value
}

func pathsOverlap(first, second string) bool {
	return pathWithin(first, second) || pathWithin(second, first)
}

func pathWithin(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func (r *RootRegistry) Get(id string) (*MediaRoot, error) {
	root, ok := r.byID[id]
	if !ok || id == "" {
		return nil, ErrUnknownRoot
	}
	return root, nil
}

func (r *RootRegistry) Roots() []*MediaRoot {
	return append([]*MediaRoot(nil), r.ordered...)
}

func (r *RootRegistry) Summaries() []RootSummary {
	result := make([]RootSummary, 0, len(r.ordered))
	for _, root := range r.ordered {
		result = append(result, RootSummary{ID: root.ID, Label: root.Label})
	}
	return result
}

func (r *RootRegistry) Redact(line string) string {
	type replacement struct{ path, label string }
	replacements := make([]replacement, 0, len(r.ordered))
	for _, root := range r.ordered {
		replacements = append(replacements, replacement{path: root.Root.Path(), label: "[" + root.Label + "]"})
	}
	sort.Slice(replacements, func(i, j int) bool { return len(replacements[i].path) > len(replacements[j].path) })
	for _, replacement := range replacements {
		line = strings.ReplaceAll(line, replacement.path, replacement.label)
	}
	return line
}
