package mcpcli

import "testing"

// TestWantsMCP pins the --mcp entry detection: the switch is matched exactly,
// so lookalike flags never select the stdio service.
func TestWantsMCP(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "exact flag", args: []string{"--mcp"}, want: true},
		{name: "flag after binary", args: []string{"nodeshell", "--mcp"}, want: true},
		{name: "flag in trailing position", args: []string{"--verbose", "--mcp"}, want: true},
		{name: "no args", args: nil, want: false},
		{name: "similar flag is not a match", args: []string{"--mcp-extra"}, want: false},
		{name: "unrelated flags", args: []string{"--help", "--version"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WantsMCP(tc.args); got != tc.want {
				t.Fatalf("WantsMCP(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
