package settings

import (
	"testing"
	"time"
)

// A translation is given from 1 to 300 seconds, and 0, which is what a
// settings.json without it reads as, is the default: 30, room for a long
// reply to be translated.
func TestTranslateTimeoutKeptAndBounded(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if w := Load().TranslateWait(); w != 30*time.Second {
		t.Fatalf("unset: %v", w)
	}
	for _, n := range []int{1, 45, 300} {
		if err := Save(Settings{TranslateTimeout: n}); err != nil {
			t.Fatalf("%d: %v", n, err)
		}
		if s := Load(); s.TranslateTimeout != n || s.TranslateWait() != time.Duration(n)*time.Second {
			t.Fatalf("%d read back as %d, %v", n, s.TranslateTimeout, s.TranslateWait())
		}
	}
	for _, n := range []int{-1, 301, 600} {
		if Save(Settings{TranslateTimeout: n}) == nil {
			t.Fatalf("%d seconds accepted", n)
		}
	}
	if Load().TranslateTimeout != 300 {
		t.Fatal("a refused value overwrote the saved one")
	}
	// a settings.json written by hand past the bounds still waits within them
	if w := (Settings{TranslateTimeout: 600}).TranslateWait(); w != 300*time.Second {
		t.Fatalf("600 waits %v", w)
	}
	if w := (Settings{TranslateTimeout: -3}).TranslateWait(); w != 30*time.Second {
		t.Fatalf("-3 waits %v", w)
	}
}
