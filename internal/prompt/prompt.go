package prompt

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"lambda/internal/skills"
)

const basePrompt = `You are lambda, a terse CLI coding assistant running in a terminal on the user's machine. You have tools to read and edit files and to run bash. Use them proactively to answer questions and complete tasks — don't ask permission, just act.

Guidelines:
- Prefer edit over write when modifying existing files. Pick an old_string with enough surrounding context to be unique.
- Prefer grep and glob over shelling out to ` + "`bash grep`" + ` or ` + "`bash find`" + `. They skip .git/node_modules/vendor and are budget-aware.
- Use bash for anything filesystem- or git-related the structured tools don't cover. Commands run non-interactively with empty stdin and a 120s timeout; don't start interactive programs or long-running servers.
- When a tool returns an error, read it carefully and try a different approach. A "schema error:" prefix means fix your arguments; "error:" means the call ran but failed.
- Be terse. No preamble, no trailing summaries. The user sees your tool calls and their results.
- When you're done with the task, stop calling tools and give a one-line answer.`

// Build assembles the system prompt, embedding environment context (cwd, OS,
// git status) and a listing of available skills. skillIdx may be nil or empty;
// the skills block is omitted when no skills are loaded.
func Build(cwd string, skillIdx *skills.Index) string {
	var uname, gitStatus string
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); uname = shellOut("uname", "-a") }()
	go func() { defer wg.Done(); gitStatus = shellOut("git", "status", "--short", "--branch") }()
	wg.Wait()

	var b strings.Builder
	b.WriteString(basePrompt)
	b.WriteString("\n\n<environment>\n")
	fmt.Fprintf(&b, "cwd: %s\n", cwd)
	fmt.Fprintf(&b, "os: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if uname != "" {
		fmt.Fprintf(&b, "uname: %s\n", uname)
	}
	if gitStatus != "" {
		fmt.Fprintf(&b, "git:\n%s\n", indent(truncateLines(gitStatus, 40), "  "))
	}
	b.WriteString("</environment>")
	if block := skillsBlock(skillIdx); block != "" {
		b.WriteString("\n\n")
		b.WriteString(block)
	}
	return b.String()
}

// skillsBlock renders the available-skills listing (name + description) for
// inclusion in the system prompt. Returns "" when no skills are loaded so the
// prompt stays clean for users without a skills directory.
func skillsBlock(idx *skills.Index) string {
	if idx == nil {
		return ""
	}
	list := idx.List()
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<available-skills>\n")
	b.WriteString("Each skill is a markdown instruction pack. Load one by calling the `skill` tool with its name. Skills are listed below as `name: description`.\n\n")
	for _, s := range list {
		fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
	}
	b.WriteString("</available-skills>")
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

// truncateLines keeps at most maxLines lines of s, appending a count of the
// elided ones. Used for environment context like `git status` which can balloon
// in dirty repos.
func truncateLines(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n") + fmt.Sprintf("\n… (%d more lines)", len(lines)-maxLines)
}
