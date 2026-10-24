package playlist

import (
	"testing"
)

func TestNaturalSort(t *testing.T) {
	items := []FileItem{
		{Name: "Episode 10.mp4", Path: "Season 1/Episode 10.mp4"},
		{Name: "Episode 1.mp4", Path: "Season 1/Episode 1.mp4"},
		{Name: "Episode 2.mp4", Path: "Season 1/Episode 2.mp4"},
		{Name: "Episode 20.mp4", Path: "Season 1/Episode 20.mp4"},
	}

	SortNatural(items)

	expected := []string{
		"Season 1/Episode 1.mp4",
		"Season 1/Episode 2.mp4",
		"Season 1/Episode 10.mp4",
		"Season 1/Episode 20.mp4",
	}

	for i, item := range items {
		if item.Path != expected[i] {
			t.Errorf("at index %d: expected %s, got %s", i, expected[i], item.Path)
		}
	}
}

func TestExtractLanguage(t *testing.T) {
	tests := []struct {
		filename string
		expected string
	}{
		{"movie.en.srt", "en"},
		{"movie.eng.srt", "eng"},
		{"movie.spa.vtt", "spa"},
		{"[Subs] Movie [fre].ass", "fre"},
		{"subtitles.srt", "sub"},
	}

	for _, tt := range tests {
		got := extractLanguage(tt.filename)
		if got != tt.expected {
			t.Errorf("extractLanguage(%q) = %q, expected %q", tt.filename, got, tt.expected)
		}
	}
}
