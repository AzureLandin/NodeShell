//go:build !windows && !darwin

package permission

import (
	"errors"
	"os/exec"
)

func platformPrompt(req Request) Decision {
	body := nativeBody(req)
	zenity := exec.Command("zenity", "--question", "--title=NodeShell", "--width=420", "--text", body)
	if err := zenity.Run(); err == nil {
		return DecisionAllowOnce
	} else if !errors.Is(err, exec.ErrNotFound) {
		return DecisionDeny
	}
	kdialog := exec.Command("kdialog", "--yesno", body, "--title", "NodeShell")
	if kdialog.Run() == nil {
		return DecisionAllowOnce
	}
	return DecisionDeny
}
