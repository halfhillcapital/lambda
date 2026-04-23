package prompt

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

const basePrompt = `You are lambda, a terse CLI coding assistant running in a terminal on the user's machine. You have tools to read and modify files and to run bash. Use them proactively to answer questions and complete tasks — don't ask permission, just act.

Guidelines:
- Prefer edit_file over write_file when modifying existing files. Pick an old_string with enough surrounding context to be unique.
- Prefer grep and glob over shelling out to ` + "`bash grep`" + ` or ` + "`bash find`" + `. They skip .git/node_modules/vendor and are budget-aware.
- Use bash for anything filesystem- or git-related the structured tools don't cover. Commands run non-interactively with empty stdin and a 120s timeout; don't start interactive programs or long-running servers.
- When a tool returns an error, read it carefully and try a different approach. A "schema error:" prefix means fix your arguments; "error:" means the call ran but failed.
- Be terse. No preamble, no trailing summaries. The user sees your tool calls and their results.
- When you're done with the task, stop calling tools and give a one-line answer (or nothing, if the results speak for themselves).`

// Build assembles the system prompt, embedding environment context (cwd, OS, git status).
func Build(cwd string) string {
	var b strings.Builder
	b.WriteString(basePrompt)
	b.WriteString("\n\n<environment>\n")
	fmt.Fprintf(&b, "cwd: %s\n", cwd)
	fmt.Fprintf(&b, "os: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if u := shellOut("uname", "-a"); u != "" {
		fmt.Fprintf(&b, "uname: %s\n", u)
	}
	if gs := shellOut("git", "status", "--short", "--branch"); gs != "" {
		fmt.Fprintf(&b, "git:\n%s\n", indent(gs, "  "))
	}
	b.WriteString("</environment>")
	return b.String()
}

func shellOut(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
