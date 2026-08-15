package main

import (
	"context"
	"errors"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func TestInitialWindowSizeTable(t *testing.T) {
	cases := []struct {
		name   string
		sw, sh int
		wantW  int
		wantH  int
	}{
		{name: "1080p", sw: 1920, sh: 1080, wantW: 1536, wantH: 864},
		{name: "768-high laptop", sw: 1366, sh: 768, wantW: 1092, wantH: 614},
		{name: "HiDPI logical 1280x720", sw: 1280, sh: 720, wantW: 1024, wantH: 576},
		{name: "4K logical", sw: 3840, sh: 2160, wantW: 3072, wantH: 1728},
		{name: "zero width falls back", sw: 0, sh: 1080, wantW: fallbackWindowWidth, wantH: fallbackWindowHeight},
		{name: "zero height falls back", sw: 1920, sh: 0, wantW: fallbackWindowWidth, wantH: fallbackWindowHeight},
		{name: "negative falls back", sw: -1, sh: -1, wantW: fallbackWindowWidth, wantH: fallbackWindowHeight},
		// 600x400: 92% cap is 552x368; min is lowered to that cap so the
		// window stays on-screen instead of the 720x480 design floor.
		{name: "smaller than design min", sw: 600, sh: 400, wantW: 552, wantH: 368},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotW, gotH := initialWindowSize(tc.sw, tc.sh)
			if gotW != tc.wantW || gotH != tc.wantH {
				t.Fatalf("initialWindowSize(%d, %d) = %dx%d, want %dx%d",
					tc.sw, tc.sh, gotW, gotH, tc.wantW, tc.wantH)
			}
			if tc.sw > 0 && tc.sh > 0 {
				if gotW > tc.sw || gotH > tc.sh {
					t.Fatalf("initialWindowSize(%d, %d) = %dx%d exceeds the screen",
						tc.sw, tc.sh, gotW, gotH)
				}
			}
		})
	}
}

func screenSize(w, h int, current, primary bool) runtime.Screen {
	var s runtime.Screen
	s.Size.Width = w
	s.Size.Height = h
	s.IsCurrent = current
	s.IsPrimary = primary
	return s
}

func TestScreenLogicalSizePrefersCurrentThenPrimary(t *testing.T) {
	screens := []runtime.Screen{
		screenSize(1920, 1080, false, true),
		screenSize(1280, 720, true, false),
		screenSize(800, 600, false, false),
	}
	w, h, ok := screenLogicalSize(screens)
	if !ok || w != 1280 || h != 720 {
		t.Fatalf("screenLogicalSize = %dx%d ok=%v, want current 1280x720", w, h, ok)
	}

	primaryOnly := []runtime.Screen{
		screenSize(800, 600, false, false),
		screenSize(1920, 1080, false, true),
	}
	w, h, ok = screenLogicalSize(primaryOnly)
	if !ok || w != 1920 || h != 1080 {
		t.Fatalf("screenLogicalSize = %dx%d ok=%v, want primary 1920x1080", w, h, ok)
	}

	firstOnly := []runtime.Screen{
		screenSize(1024, 768, false, false),
	}
	w, h, ok = screenLogicalSize(firstOnly)
	if !ok || w != 1024 || h != 768 {
		t.Fatalf("screenLogicalSize = %dx%d ok=%v, want first 1024x768", w, h, ok)
	}

	if _, _, ok = screenLogicalSize(nil); ok {
		t.Fatal("empty screens must not be ok")
	}
}

func TestScreenLogicalSizeFallsBackToDeprecatedWidthHeight(t *testing.T) {
	screens := []runtime.Screen{
		{IsCurrent: true, Width: 1366, Height: 768},
	}
	w, h, ok := screenLogicalSize(screens)
	if !ok || w != 1366 || h != 768 {
		t.Fatalf("screenLogicalSize = %dx%d ok=%v, want deprecated 1366x768", w, h, ok)
	}

	if _, _, ok = screenLogicalSize([]runtime.Screen{{IsCurrent: true}}); ok {
		t.Fatal("zero-size screen must not be ok")
	}
}

func TestApplyInitialWindowSizeAlwaysShows(t *testing.T) {
	origGet, origSet, origMin, origCenter, origShow := screenGetAll, windowSetSize, windowSetMinSize, windowCenter, windowShow
	t.Cleanup(func() {
		screenGetAll, windowSetSize, windowSetMinSize, windowCenter, windowShow = origGet, origSet, origMin, origCenter, origShow
	})

	type result struct {
		shown  bool
		setW   int
		setH   int
		minW   int
		minH   int
		center int
	}

	run := func(get func(context.Context) ([]runtime.Screen, error)) result {
		var got result
		screenGetAll = get
		windowSetSize = func(_ context.Context, w, h int) { got.setW, got.setH = w, h }
		windowSetMinSize = func(_ context.Context, w, h int) { got.minW, got.minH = w, h }
		windowCenter = func(context.Context) { got.center++ }
		windowShow = func(context.Context) { got.shown = true }
		applyInitialWindowSize(context.Background())
		return got
	}

	ok := run(func(context.Context) ([]runtime.Screen, error) {
		return []runtime.Screen{screenSize(1920, 1080, true, false)}, nil
	})
	if !ok.shown || ok.center != 1 || ok.setW != 1536 || ok.setH != 864 {
		t.Fatalf("success path = %+v, want shown+centered 1536x864", ok)
	}
	if ok.minW != minWindowWidth || ok.minH != minWindowHeight {
		t.Fatalf("success min size = %dx%d, want %dx%d", ok.minW, ok.minH, minWindowWidth, minWindowHeight)
	}

	failed := run(func(context.Context) ([]runtime.Screen, error) {
		return nil, errors.New("no screens")
	})
	if !failed.shown || failed.center != 1 || failed.setW != fallbackWindowWidth || failed.setH != fallbackWindowHeight {
		t.Fatalf("error path = %+v, want shown+centered fallback", failed)
	}

	empty := run(func(context.Context) ([]runtime.Screen, error) {
		return nil, nil
	})
	if !empty.shown || empty.setW != fallbackWindowWidth || empty.setH != fallbackWindowHeight {
		t.Fatalf("empty screens = %+v, want shown fallback", empty)
	}
}
