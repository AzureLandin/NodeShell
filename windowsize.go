package main

import (
	"context"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	fallbackWindowWidth  = 1200
	fallbackWindowHeight = 800
	minWindowWidth       = 720
	minWindowHeight      = 480
	windowScreenRatio    = 0.80
	windowScreenMaxRatio = 0.92
)

// screenGetAll / window* are seams so applyInitialWindowSize can be tested
// without a WebView. Production points them at the Wails runtime.
var (
	screenGetAll     = runtime.ScreenGetAll
	windowSetSize    = runtime.WindowSetSize
	windowSetMinSize = runtime.WindowSetMinSize
	windowCenter     = runtime.WindowCenter
	windowShow       = runtime.WindowShow
)

// initialWindowSize returns a window size that is 80% of the logical screen,
// clamped to [min, 92% of screen]. When the screen is smaller than the design
// minimum, the result never exceeds the 92% cap (stay on-screen).
func initialWindowSize(screenW, screenH int) (width, height int) {
	if screenW <= 0 || screenH <= 0 {
		return fallbackWindowWidth, fallbackWindowHeight
	}
	maxW := max(1, int(float64(screenW)*windowScreenMaxRatio))
	maxH := max(1, int(float64(screenH)*windowScreenMaxRatio))
	minW := min(minWindowWidth, maxW)
	minH := min(minWindowHeight, maxH)
	w := int(float64(screenW) * windowScreenRatio)
	h := int(float64(screenH) * windowScreenRatio)
	return clampInt(w, minW, maxW), clampInt(h, minH, maxH)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// screenLogicalSize picks the current screen (else primary, else first) and
// returns its logical Size, falling back to the deprecated Width/Height.
func screenLogicalSize(screens []runtime.Screen) (w, h int, ok bool) {
	s, ok := pickScreen(screens)
	if !ok {
		return 0, 0, false
	}
	w, h = s.Size.Width, s.Size.Height
	if w <= 0 || h <= 0 {
		w, h = s.Width, s.Height
	}
	if w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

func pickScreen(screens []runtime.Screen) (runtime.Screen, bool) {
	if len(screens) == 0 {
		return runtime.Screen{}, false
	}
	var primary *runtime.Screen
	for i := range screens {
		if screens[i].IsCurrent {
			return screens[i], true
		}
		if screens[i].IsPrimary && primary == nil {
			primary = &screens[i]
		}
	}
	if primary != nil {
		return *primary, true
	}
	return screens[0], true
}

// applyInitialWindowSize sizes and centers the hidden window from the current
// screen, then always shows it — ScreenGetAll failures fall back to 1200x800.
func applyInitialWindowSize(ctx context.Context) {
	defer windowShow(ctx)
	w, h := fallbackWindowWidth, fallbackWindowHeight
	if screens, err := screenGetAll(ctx); err == nil {
		if sw, sh, ok := screenLogicalSize(screens); ok {
			w, h = initialWindowSize(sw, sh)
		}
	}
	windowSetMinSize(ctx, min(minWindowWidth, w), min(minWindowHeight, h))
	windowSetSize(ctx, w, h)
	windowCenter(ctx)
}
