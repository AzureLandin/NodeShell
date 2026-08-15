//go:build darwin

package permission

import (
	"os/exec"
	"strings"
)

func platformPrompt(req Request) Decision {
	script := "display dialog " + appleString(nativeBody(req)) +
		` with title "NodeShell" buttons {"Deny","Allow"} default button "Deny" with icon caution`
	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return DecisionDeny
	}
	if strings.Contains(string(out), "Allow") {
		return DecisionAllowOnce
	}
	return DecisionDeny
}

func appleString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
