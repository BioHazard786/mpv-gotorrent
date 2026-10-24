package playlist

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/anacrolix/torrent"
)

// Supported video extensions.
var videoExtensions = map[string]bool{
	".mkv":  true,
	".mp4":  true,
	".avi":  true,
	".webm": true,
	".mov":  true,
	".flv":  true,
	".wmv":  true,
	".m4v":  true,
	".ts":   true,
	".m2ts": true,
	".vob":  true,
	".ogv":  true,
}

// Supported subtitle extensions.
var subtitleExtensions = map[string]bool{
	".srt": true,
	".vtt": true,
	".ass": true,
	".ssa": true,
	".sub": true,
}

// FileItem represents a torrent file enriched with metadata.
type FileItem struct {
	Index        int           `json:"index"`
	Path         string        `json:"path"`
	Name         string        `json:"name"`
	Size         int64         `json:"size"`
	IsVideo      bool          `json:"is_video"`
	IsSubtitle   bool          `json:"is_subtitle"`
	StreamURL    string        `json:"stream_url,omitempty"`
	Subtitles    []SubtitleItem `json:"subtitles,omitempty"`
	TorrentFile  *torrent.File `json:"-"`
}

// SubtitleItem represents an associated subtitle.
type SubtitleItem struct {
	Index       int    `json:"index"`
	Name        string `json:"name"`
	Language    string `json:"language,omitempty"`
	DownloadURL string `json:"download_url"`
}

// ParseFiles categorizes and prepares torrent files.
func ParseFiles(files []*torrent.File, baseURL string) ([]FileItem, []FileItem) {
	var allFiles []FileItem
	var videoFiles []FileItem
	var subFiles []FileItem

	for idx, f := range files {
		dispPath := f.DisplayPath()
		ext := strings.ToLower(filepath.Ext(dispPath))
		baseName := filepath.Base(dispPath)

		isVideo := videoExtensions[ext]
		isSub := subtitleExtensions[ext]

		item := FileItem{
			Index:       idx,
			Path:        dispPath,
			Name:        baseName,
			Size:        f.Length(),
			IsVideo:     isVideo,
			IsSubtitle:  isSub,
			TorrentFile: f,
		}

		allFiles = append(allFiles, item)
		if isVideo {
			videoFiles = append(videoFiles, item)
		} else if isSub {
			subFiles = append(subFiles, item)
		}
	}

	// If no recognized video file extension was found, but there are large files (> 50MB),
	// treat the largest files as playable media.
	if len(videoFiles) == 0 && len(allFiles) > 0 {
		for _, f := range allFiles {
			if f.Size > 50*1024*1024 && !f.IsSubtitle {
				f.IsVideo = true
				videoFiles = append(videoFiles, f)
			}
		}
	}

	// Sort video files in natural order (e.g. S01E01, S01E02, Episode 1, Episode 2, etc.)
	SortNatural(videoFiles)

	// Associate subtitles with video files
	for i := range videoFiles {
		videoFiles[i].StreamURL = baseURL + "/stream?file=" + strconv.Itoa(videoFiles[i].Index)
		videoFiles[i].Subtitles = findMatchingSubtitles(videoFiles[i], subFiles, baseURL)
	}

	return allFiles, videoFiles
}

// findMatchingSubtitles matches subtitles located in same directory or with similar prefix.
func findMatchingSubtitles(video FileItem, subs []FileItem, baseURL string) []SubtitleItem {
	var matched []SubtitleItem
	videoDir := filepath.Dir(video.Path)
	videoStem := strings.TrimSuffix(video.Name, filepath.Ext(video.Name))

	for _, sub := range subs {
		subDir := filepath.Dir(sub.Path)
		subStem := strings.TrimSuffix(sub.Name, filepath.Ext(sub.Name))

		// Check if in same folder or "Subs" subfolder
		isRelevantFolder := subDir == videoDir ||
			strings.EqualFold(filepath.Base(subDir), "subs") ||
			strings.EqualFold(filepath.Base(subDir), "subtitles")

		if !isRelevantFolder && len(subs) > 5 {
			continue
		}

		// Check similarity or if this is the only video / only sub
		if strings.HasPrefix(strings.ToLower(subStem), strings.ToLower(videoStem)) || len(subs) == 1 {
			lang := extractLanguage(subStem)
			matched = append(matched, SubtitleItem{
				Index:       sub.Index,
				Name:        sub.Name,
				Language:    lang,
				DownloadURL: baseURL + "/sub?file=" + strconv.Itoa(sub.Index),
			})
		}
	}

	return matched
}

// extractLanguage tries to determine language code from filename (e.g. .en.srt, .eng.srt, [eng].ass)
var langRegex = regexp.MustCompile(`(?i)[._ \-\[\(]([a-z]{2,3})(?:[._ \-\]\)]|$)`)

func extractLanguage(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if subtitleExtensions[ext] {
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	matches := langRegex.FindAllStringSubmatch(name, -1)
	if len(matches) > 0 {
		last := matches[len(matches)-1]
		if len(last) > 1 {
			candidate := strings.ToLower(last[1])
			if !subtitleExtensions["."+candidate] {
				return candidate
			}
		}
	}
	return "sub"
}

// SortNatural performs human-friendly natural sorting on a slice of FileItems.
func SortNatural(items []FileItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return naturalLess(items[i].Path, items[j].Path)
	})
}

// naturalLess compares two strings chunking digit runs as numbers.
func naturalLess(s1, s2 string) bool {
	chunks1 := chunkString(s1)
	chunks2 := chunkString(s2)

	n := len(chunks1)
	if len(chunks2) < n {
		n = len(chunks2)
	}

	for i := 0; i < n; i++ {
		c1 := chunks1[i]
		c2 := chunks2[i]

		if c1.isNum && c2.isNum {
			if c1.numVal != c2.numVal {
				return c1.numVal < c2.numVal
			}
		} else {
			cmp := strings.Compare(strings.ToLower(c1.strVal), strings.ToLower(c2.strVal))
			if cmp != 0 {
				return cmp < 0
			}
		}
	}

	return len(chunks1) < len(chunks2)
}

type chunk struct {
	isNum  bool
	numVal uint64
	strVal string
}

func chunkString(s string) []chunk {
	var chunks []chunk
	runes := []rune(s)
	idx := 0

	for idx < len(runes) {
		if unicode.IsDigit(runes[idx]) {
			start := idx
			for idx < len(runes) && unicode.IsDigit(runes[idx]) {
				idx++
			}
			numStr := string(runes[start:idx])
			val, _ := strconv.ParseUint(numStr, 10, 64)
			chunks = append(chunks, chunk{isNum: true, numVal: val, strVal: numStr})
		} else {
			start := idx
			for idx < len(runes) && !unicode.IsDigit(runes[idx]) {
				idx++
			}
			chunks = append(chunks, chunk{isNum: false, strVal: string(runes[start:idx])})
		}
	}

	return chunks
}
