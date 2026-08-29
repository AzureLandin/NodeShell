package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"nodeshell/internal/apperror"
	"nodeshell/internal/mcpcli"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	os.Exit(run(os.Args[1:]))
}

// mode distinguishes the two process entries.
type mode int

const (
	modeGUI mode = iota
	modeMCP
)

// run dispatches the process entry. --mcp runs the native MCP stdio service
// without initialising the WebView; anything else starts the Wails GUI.
func run(args []string) int {
	switch entryMode(args) {
	case modeMCP:
		return runMCP()
	default:
		return runGUI()
	}
}

// entryMode decides the process entry from args. The --mcp switch is matched
// exactly; lookalikes (e.g. --mcp-extra) fall through to the GUI.
func entryMode(args []string) mode {
	if mcpcli.WantsMCP(args) {
		return modeMCP
	}
	return modeGUI
}

// mcpIO is the --mcp stdio seam. Production serves the real OS streams via
// the mcpcli wiring; tests inject in-memory streams so the entry contract can
// be exercised without touching the user profile or blocking on stdin.
var mcpIO = func(ctx context.Context, in io.Reader, out, errOut io.Writer) error {
	return mcpcli.RunMCP(ctx, in, out, errOut)
}

// runMCP is the --mcp branch: it serves the native MCP stdio protocol on
// stdin/stdout until EOF or an interrupt, then exits. The WebView is never
// initialised on this branch and nothing but protocol responses is written
// to stdout.
func runMCP() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcpIO(ctx, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// runGUI starts the Wails desktop application.
func runGUI() int {
	// Windows CI/release builds use -windowsconsole so --mcp can use redirected
	// stdin/stdout. Detach that console for the GUI entry so a terminal does
	// not flash when the user launches the desktop app.
	detachConsole()
	app := NewApp()
	err := wails.Run(&options.App{
		Title:     "NodeShell",
		Width:     fallbackWindowWidth,
		Height:    fallbackWindowHeight,
		MinWidth:  minWindowWidth,
		MinHeight: minWindowHeight,
		// Hidden until applyInitialWindowSize has clamped to 80% of the
		// current screen, so a 1200x800 fallback never flashes off-screen.
		StartHidden: true,
		// Match theme.css --bg-app dark default so the native window is not
		// white for a frame before the WebView paints (startup flash).
		BackgroundColour: options.NewRGB(0x12, 0x12, 0x12),
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// EnableFileDrop allows Wails to resolve absolute paths of dropped files
		// when the frontend registers window.runtime.OnFileDrop. The SftpPanel
		// uploads them into its current session. DisableWebViewDrop stays false
		// so the WebView's own drop handling (and the DOM drag visuals) keep
		// working.
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop:     true,
			DisableWebViewDrop: false,
		},
		OnStartup: func(ctx context.Context) {
			app.startup(ctx)
			applyInitialWindowSize(ctx)
		},
		// Dispose all SSH sessions and in-flight connects before the WebView
		// tears down, so no goroutine outlives the runtime context.
		OnShutdown: app.shutdown,
		// Encode domain error codes as NODESHELL_ERR:<CODE>:<message> so the
		// frontend parser (src/shared/ipc-error.ts) recovers the code; the
		// default formatter would strip it down to the message only.
		ErrorFormatter: apperror.Format,
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
