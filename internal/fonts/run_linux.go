//go:build linux

package fonts

import "context"

// platformList enumerates Linux fonts via fc-list, one family per line.
func platformList(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	out, err := runCommand(ctx, MaxOutputBytes, "fc-list", "-f", "%{family[0]}\n")
	if err != nil {
		return nil, err
	}
	return parseLinux(string(out))
}
