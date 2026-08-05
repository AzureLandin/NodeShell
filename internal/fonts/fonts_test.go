package fonts

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// --- normalize (shared trim/dedupe/sort) ---

func TestNormalizeTrimsDedupesAndSortsCaseInsensitively(t *testing.T) {
	in := []string{
		"  Arial  ",
		"consolas",
		"Consolas",
		"宋体",
		"",
		"  ",
		"DejaVu Sans Mono",
		"arial",
	}
	got := normalize(in)
	// Case-insensitive order; a case-fold tie is broken by the original value
	// (Go's prescribed sort.Slice approach — uppercase sorts first).
	want := []string{"Arial", "arial", "Consolas", "consolas", "DejaVu Sans Mono", "宋体"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalize = %#v, want %#v", got, want)
	}
	// The input slice must not be mutated by normalize.
	if !reflect.DeepEqual(in, []string{"  Arial  ", "consolas", "Consolas", "宋体", "", "  ", "DejaVu Sans Mono", "arial"}) {
		t.Fatalf("normalize mutated its input: %#v", in)
	}
}

func TestNormalizeStripsSurroundingQuotes(t *testing.T) {
	got := normalize([]string{`"Quoted Name"`, `'Single Quoted'`, "plain", `"Quoted Name"`})
	want := []string{"plain", "Quoted Name", "Single Quoted"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalize = %#v, want %#v", got, want)
	}
}

// --- windows parsers ---

func TestParsePresentationCoreHandlesChineseCRLFQuotesAndPaths(t *testing.T) {
	const out = "Arial\r\n" +
		"宋体\r\n" +
		"Noto Sans CJK SC\r\n" +
		"\"Quoted Name\"\r\n" +
		"\r\n" +
		"Arial\r\n" +
		"C:\\Windows\\Fonts\\arial.ttf\r\n" +
		"\\\\server\\share\\font.otf\r\n" +
		"/etc/fonts/font.ttf\r\n" +
		"  padded  \r\n"
	got, err := parsePresentationCore(out)
	if err != nil {
		t.Fatalf("parsePresentationCore: %v", err)
	}
	// CRLF is trimmed, blank lines dropped, file paths never become families;
	// duplicates are preserved at parse level (dedupe happens in normalize).
	want := []string{"Arial", "宋体", "Noto Sans CJK SC", `"Quoted Name"`, "Arial", "padded"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePresentationCore = %#v, want %#v", got, want)
	}
}

func TestParseRegistryStripsFontMetadataSuffixes(t *testing.T) {
	const out = "Arial (TrueType)\r\n" +
		"Courier New (TrueType)\r\n" +
		"Modern (All res)\r\n" +
		"Small Fonts (VGA res)\r\n" +
		"Some Family (OpenType)\r\n" +
		"C:\\Windows\\Fonts\\arial.ttf\r\n" +
		"\r\n" +
		"Segoe UI Variable Display (TrueType)\r\n"
	got, err := parseRegistry(out)
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	want := []string{
		"Arial",
		"Courier New",
		"Modern",
		"Small Fonts",
		"Some Family",
		"Segoe UI Variable Display",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseRegistry = %#v, want %#v", got, want)
	}
}

func TestParseRegistryKeepsLegitimateParensInNames(t *testing.T) {
	const out = "Font (Fancy) (TrueType)\r\n"
	got, err := parseRegistry(out)
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"Font (Fancy)"}) {
		t.Fatalf("parseRegistry = %#v, want %#v", got, []string{"Font (Fancy)"})
	}
}

// --- darwin parser (system_profiler -json) ---
//
// Real `system_profiler SPFontsDataType -json` output is an object wrapping
// the items, `{"SPFontsDataType": [...]}`, never a bare array. Two released
// shapes are documented: the classic item-level form with a capitalized
// "Family" key (macOS 10.x–12.x; the Electron build's font-list fallback
// greps "Family:" from the plain-text form), and a newer nested form whose
// items carry only "_name" (a file name), "path" and a lowercase "typefaces"
// array whose entries hold the lowercase "family" key (macOS 10.15 Catalina
// output; see keqingrong/system-fonts#6). The fixtures below follow those
// real shapes; `_name` (file name / PostScript name) and `path` must never
// become families.

const classicFontsJSON = `{
  "SPFontsDataType" : [
    {
      "_name" : "Arial",
      "type" : "FontsDataType",
      "path" : "/System/Library/Fonts/Supplemental/Arial.ttf",
      "Family" : "Arial",
      "Style" : "Regular",
      "Version" : "5.15",
      "UniqueName" : "Arial; Regular",
      "Enabled" : "yes",
      "PostScriptName" : "ArialMT"
    },
    {
      "_name" : "PingFang SC",
      "type" : "FontsDataType",
      "path" : "/System/Library/Fonts/PingFang.ttc",
      "Family" : "PingFang SC",
      "Style" : "Regular",
      "Enabled" : "yes"
    },
    {
      "_name" : "Noto Sans",
      "type" : "FontsDataType",
      "path" : "/Library/Fonts/Noto Sans.ttc",
      "Family" : "  Noto Sans  ",
      "Style" : "Regular"
    },
    {
      "_name" : "STHeiti Light",
      "type" : "FontsDataType",
      "path" : "/System/Library/Fonts/STHeiti Light.ttc"
    }
  ]
}`

const nestedFontsJSON = `{
  "SPFontsDataType" : [
    {
      "_name" : "SFNSDisplayCondensed-Thin.otf",
      "enabled" : "yes",
      "path" : "/System/Library/Fonts/SFNSDisplayCondensed-Thin.otf",
      "type" : "opentype",
      "typefaces" : [
        { "_name" : ".SFNSDisplayCondensed-Thin", "family" : "SFNS Display Condensed", "style" : "Thin" },
        { "_name" : ".SFNSDisplayCondensed-Regular", "family" : "SFNS Display Condensed", "style" : "Regular" }
      ]
    },
    {
      "_name" : "Hack Regular.ttf",
      "path" : "/Library/Fonts/Hack Regular.ttf",
      "type" : "truetype",
      "typefaces" : [
        { "_name" : "Hack-Regular", "family" : "Hack", "style" : "Regular" },
        { "_name" : "Hack-Bold", "style" : "Bold" }
      ]
    },
    {
      "_name" : "orphan.ttf",
      "path" : "/tmp/orphan.ttf",
      "type" : "truetype"
    }
  ]
}`

func TestParseDarwinClassicItemLevelFamily(t *testing.T) {
	got, err := parseDarwin(classicFontsJSON)
	if err != nil {
		t.Fatalf("parseDarwin: %v", err)
	}
	// Item-level "Family" wins, surrounding spaces are trimmed, and an item
	// without a family (only "_name"/path, e.g. a hidden system font) is
	// skipped — its file name must never surface as a family.
	want := []string{"Arial", "PingFang SC", "Noto Sans"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDarwin = %#v, want %#v", got, want)
	}
}

func TestParseDarwinNestedTypefaceFamilies(t *testing.T) {
	got, err := parseDarwin(nestedFontsJSON)
	if err != nil {
		t.Fatalf("parseDarwin: %v", err)
	}
	// Duplicates are preserved at parse level (dedupe happens in normalize).
	// A typeface exposing only a style (no family) must not be promoted to a
	// family name; the "_name" PostScript names and file names are ignored.
	want := []string{"SFNS Display Condensed", "SFNS Display Condensed", "Hack"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDarwin = %#v, want %#v", got, want)
	}
}

func TestParseDarwinEmptyWrapperIsLegal(t *testing.T) {
	got, err := parseDarwin(`{"SPFontsDataType": []}`)
	if err != nil {
		t.Fatalf("parseDarwin of an empty font list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("parseDarwin of an empty font list = %#v, want empty", got)
	}
}

func TestParseDarwinRejectsBareArray(t *testing.T) {
	// A bare top-level array is not a shape any released macOS emits (the old
	// fake fixture); it must error rather than parse.
	if _, err := parseDarwin(`[{"_name":"Arial","Family":"Arial"}]`); err == nil {
		t.Fatal("parseDarwin must reject a bare top-level array")
	}
}

func TestParseDarwinRejectsMalformedJSON(t *testing.T) {
	if _, err := parseDarwin("{not json"); err == nil {
		t.Fatal("parseDarwin must reject malformed JSON")
	}
}

// --- linux parser (fc-list) ---

func TestParseLinuxSplitsLinesAndDropsBlanks(t *testing.T) {
	const out = "DejaVu Sans Mono\nNoto Sans CJK SC\n\n  Space Name  \n"
	got, err := parseLinux(out)
	if err != nil {
		t.Fatalf("parseLinux: %v", err)
	}
	want := []string{"DejaVu Sans Mono", "Noto Sans CJK SC", "Space Name"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseLinux = %#v, want %#v", got, want)
	}
}

// --- runCommand runner ---

func TestRunCommandCapturesStdout(t *testing.T) {
	name, args := echoCommand()
	out, err := runCommand(context.Background(), MaxOutputBytes, name, args...)
	if err != nil {
		t.Fatalf("runCommand: %v", err)
	}
	if !strings.Contains(string(out), "hello") {
		t.Fatalf("runCommand output = %q, want it to contain hello", out)
	}
}

func TestRunCommandReportsStartFailure(t *testing.T) {
	if _, err := runCommand(context.Background(), MaxOutputBytes, "definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatal("runCommand with a missing executable must error")
	}
}

func TestRunCommandTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	name, args := slowCommand()
	_, err := runCommand(ctx, MaxOutputBytes, name, args...)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runCommand over a deadlined ctx = %v, want context.DeadlineExceeded", err)
	}
}

func TestRunCommandCapsOutput(t *testing.T) {
	name, args := bigOutputCommand()
	_, err := runCommand(context.Background(), 1024, name, args...)
	if !errors.Is(err, errOutputExceeded) {
		t.Fatalf("runCommand over the output cap = %v, want errOutputExceeded", err)
	}
}

// --- platform helpers used by runner tests (branch on the host OS) ---

func echoCommand() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", "echo hello"}
	}
	return "sh", []string{"-c", "echo hello"}
}

func slowCommand() (string, []string) {
	// Must be a single process: a killed parent with an orphaned child would
	// hold the test's stdout pipe open until the child exits.
	if runtime.GOOS == "windows" {
		return "powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", "Start-Sleep -Seconds 10"}
	}
	return "sh", []string{"-c", "sleep 10"}
}

func bigOutputCommand() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", "type C:\\Windows\\System32\\ntoskrnl.exe"}
	}
	return "sh", []string{"-c", "cat /dev/zero | head -c 100000"}
}
