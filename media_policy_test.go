package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMediaRoutingOverridesLabels(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind ContentType
		want ContentType
		ok   bool
	}{
		{"song.MP3", Movies, Music, true}, {"song.S01E02.flac", TVShow, Music, true},
		{"Show.S01 E02.mkv", Movies, TVShow, true}, {"Show.s01.e02.mp4", Music, TVShow, true},
		{"film.mp4", Music, Movies, true}, {"episode.mkv", TVShow, TVShow, true},
		{"readme.txt", Movies, "", false}, {"movie.nfo", TVShow, "", false}, {"installer.EXE", Music, "", false},
		{"../film.mkv", Movies, "", false}, {"cover.jpg", Music, "", false},
	} {
		i := Item{Name: tc.name, Type: tc.kind}
		err := routeMedia(&i)
		if (err == nil) != tc.ok || tc.ok && i.Type != tc.want {
			t.Errorf("%q: type=%s err=%v", tc.name, i.Type, err)
		}
	}
}
func TestParentEpisodeRouting(t *testing.T) {
	i := Item{Name: "video.mkv", Type: Movies, RelativePath: "Show.S01 E02/video.mkv"}
	if err := routeMedia(&i); err != nil || i.Type != TVShow {
		t.Fatalf("%+v %v", i, err)
	}
}
func TestSupportAssociation(t *testing.T) {
	for _, tc := range []struct {
		i    Item
		want ContentType
		ok   bool
	}{
		{Item{Name: "cover.jpg", MediaName: "track.flac", MediaFileID: 7}, Music, true},
		{Item{Name: "movie.en.srt", MediaName: "movie.mkv"}, Movies, true},
		{Item{Name: "episode.ass", MediaName: "Show.S01 E02.mkv"}, TVShow, true},
		{Item{Name: "foo.exe", MediaName: "movie.mkv"}, "", false},
		{Item{Name: "captions.srt", MediaName: "track.flac"}, "", false},
	} {
		i := tc.i
		e := routeMedia(&i)
		if (e == nil) != tc.ok || tc.ok && i.Type != tc.want {
			t.Fatalf("%+v %v", i, e)
		}
	}
}

func TestSupportDestinations(t *testing.T) {
	MoviesPath = t.TempDir()
	TVShowPath = t.TempDir()
	primary := Item{Name: "Show.S01 E02.mkv", Type: Movies}
	if e := routeMedia(&primary); e != nil {
		t.Fatal(e)
	}
	want, e := mediaDestination(&primary)
	if e != nil {
		t.Fatal(e)
	}
	side := Item{Name: "Show.S01 E02.en.srt", MediaName: primary.Name, MediaFileID: 7, Type: Movies}
	statePath := filepath.Join(t.TempDir(), "state.json")
	t.Setenv("MUSIC_STATE_PATH", statePath)
	if e := routeMedia(&side); e != nil {
		t.Fatal(e)
	}
	if _, e := mediaDestination(&side); e == nil {
		t.Fatal("support published without primary")
	}
	os.MkdirAll(filepath.Dir(want), 0755)
	os.WriteFile(want, []byte("verified primary fixture"), 0644)
	digest, e := musicFileDigest(want)
	if e != nil {
		t.Fatal(e)
	}
	state, e := readMusicState(statePath)
	if e != nil {
		t.Fatal(e)
	}
	state.Receipts["7"] = musicImportRecord{Type: TVShow, FileID: 7, Name: primary.Name, Path: want, SHA256: digest}
	if e = saveMusicState(statePath, state); e != nil {
		t.Fatal(e)
	}
	got, e := mediaDestination(&side)
	if e != nil {
		t.Fatal(e)
	}
	if strings.TrimSuffix(got, ".en.srt") != strings.TrimSuffix(want, ".mkv") {
		t.Fatalf("%s vs %s", got, want)
	}
	os.WriteFile(want, []byte("changed"), 0644)
	if _, e := mediaDestination(&side); e == nil {
		t.Fatal("support accepted changed primary")
	}

}
func TestInvalidDownloadsNeverPublished(t *testing.T) {
	for _, status := range []int{200, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			MoviesPath = t.TempDir()
			TVShowPath = t.TempDir()
			CurrentJobs = map[string]*Item{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); w.Write([]byte("not a video")) }))
			defer srv.Close()
			i := Item{Name: "fake.mp4", Type: Music, URL: srv.URL, FileSize: 11}
			if e := i.StartDownload(); e == nil {
				t.Fatal("invalid video accepted")
			}
			if i.Completed {
				t.Fatal("invalid video completed")
			}
			filepath.Walk(MoviesPath, func(p string, f os.FileInfo, e error) error {
				if e == nil && !f.IsDir() {
					t.Errorf("published %s", p)
				}
				return e
			})
		})
	}
}
func TestVideoCannotMasqueradeAsAudio(t *testing.T) {
	if _, e := exec.LookPath("ffmpeg"); e != nil {
		t.Skip("ffmpeg required")
	}
	path := filepath.Join(t.TempDir(), "sample.mp4")
	if out, e := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=size=16x16:rate=1", "-f", "lavfi", "-i", "anullsrc", "-t", "1", "-c:v", "mpeg4", "-c:a", "aac", path).CombinedOutput(); e != nil {
		t.Fatalf("%s %v", out, e)
	}
	if _, e := readMusicMetadata(path); e == nil {
		t.Fatal("video accepted into music")
	}
	if e := probeVideo(path); e != nil {
		t.Fatal(e)
	}
}

func TestRepairKeepsRecoveryAndMusicTags(t *testing.T) {
	root := t.TempDir()
	MoviesPath = filepath.Join(root, "movies")
	TVShowPath = filepath.Join(root, "tv")
	music := filepath.Join(root, "music")
	for _, p := range []string{MoviesPath, TVShowPath, music} {
		if e := os.Mkdir(p, 0755); e != nil {
			t.Fatal(e)
		}
	}
	t.Setenv("MUSIC_PATH", music)
	t.Setenv("MUSIC_STATE_PATH", filepath.Join(root, "state.json"))
	t.Setenv("MEDIA_QUARANTINE_PATH", filepath.Join(root, "recovery"))
	b, e := os.ReadFile("testdata/tagged.flac")
	if e != nil {
		t.Fatal(e)
	}
	src := filepath.Join(TVShowPath, "wrong.flac")
	os.WriteFile(src, b, 0644)
	if e = repairLibraryFile(src); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(src); !os.IsNotExist(e) {
		t.Fatal("source remained")
	}
	target := filepath.Join(music, "Rock/Various Artists/Test Album/Disc 01/02 - Test Song.flac")
	got, e := os.ReadFile(target)
	if e != nil || !bytes.Equal(got, b) {
		t.Fatal("repaired bytes differ", e)
	}
	recovery, _ := filepath.Glob(filepath.Join(root, "recovery/*/tvshow/wrong.flac"))
	if len(recovery) != 1 {
		t.Fatal("missing recovery", recovery)
	}
	got, e = os.ReadFile(recovery[0])
	if e != nil || !bytes.Equal(got, b) {
		t.Fatal("recovery differs")
	}
}

func TestRepairUsesParentEpisodeMarker(t *testing.T) {
	if _, e := exec.LookPath("ffmpeg"); e != nil {
		t.Skip("ffmpeg required")
	}
	root := t.TempDir()
	MoviesPath = filepath.Join(root, "movies")
	TVShowPath = filepath.Join(root, "tv")
	t.Setenv("MEDIA_QUARANTINE_PATH", filepath.Join(root, "recovery"))
	source := filepath.Join(MoviesPath, "Show.S02 E01", "video.mkv")
	os.MkdirAll(filepath.Dir(source), 0755)
	os.Mkdir(TVShowPath, 0755)
	if out, e := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=size=16x16:rate=1", "-t", "1", "-c:v", "mpeg4", source).CombinedOutput(); e != nil {
		t.Fatalf("%s %v", out, e)
	}
	if e := repairLibraryFile(source); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(TVShowPath, "Show/Season_02/Show.S02E01.video.mkv")); e != nil {
		t.Fatal(e)
	}
}
