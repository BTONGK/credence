package adapters

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

// TestInfo describes a command recognized as a test run.
type TestInfo struct {
	Runner  string   `json:"runner"`
	Targets []string `json:"targets,omitempty"`
}

var testRunners = []struct {
	runner string
	re     *regexp.Regexp
}{
	{"pytest", regexp.MustCompile(`(?:^|[\s;&|(])(?:python[0-9.]*\s+-m\s+pytest|py\.test|pytest)(?:\s|$)`)},
	{"unittest", regexp.MustCompile(`python[0-9.]*\s+-m\s+unittest`)},
	{"go", regexp.MustCompile(`(?:^|[\s;&|(])go\s+test(?:\s|$)`)},
	{"cargo", regexp.MustCompile(`(?:^|[\s;&|(])cargo\s+(?:test|nextest)`)},
	{"jest", regexp.MustCompile(`(?:^|[\s;&|(/])jest(?:\s|$)`)},
	{"vitest", regexp.MustCompile(`(?:^|[\s;&|(/])vitest(?:\s|$)`)},
	{"mocha", regexp.MustCompile(`(?:^|[\s;&|(/])mocha(?:\s|$)`)},
	{"npm", regexp.MustCompile(`(?:^|[\s;&|(])(?:npm|pnpm|yarn|bun)\s+(?:run\s+)?test`)},
	{"bun", regexp.MustCompile(`(?:^|[\s;&|(])bun\s+test`)},
	{"deno", regexp.MustCompile(`(?:^|[\s;&|(])deno\s+test`)},
	{"rspec", regexp.MustCompile(`(?:^|[\s;&|(])(?:bundle\s+exec\s+)?rspec(?:\s|$)`)},
	{"minitest", regexp.MustCompile(`(?:^|[\s;&|(])(?:bin/)?rails\s+test|rake\s+test`)},
	{"phpunit", regexp.MustCompile(`phpunit`)},
	{"dotnet", regexp.MustCompile(`dotnet\s+test`)},
	{"maven", regexp.MustCompile(`mvnw?\s+(?:-\S+\s+)*(?:test|verify)`)},
	{"gradle", regexp.MustCompile(`gradlew?\s+(?:\S+\s+)*test`)},
	{"swift", regexp.MustCompile(`swift\s+test`)},
	{"mix", regexp.MustCompile(`mix\s+test`)},
	{"ctest", regexp.MustCompile(`(?:^|[\s;&|(])ctest(?:\s|$)`)},
	{"make", regexp.MustCompile(`(?:^|[\s;&|(])make\s+(?:\S+\s+)*(?:test|check)(?:\s|$)`)},
	{"tox", regexp.MustCompile(`(?:^|[\s;&|(])(?:tox|nox)(?:\s|$)`)},
}

// DetectTest reports whether a shell command runs tests.
func DetectTest(cmd string) *TestInfo {
	c := strings.TrimSpace(cmd)
	if c == "" || strings.HasPrefix(c, "#") {
		return nil
	}
	for _, r := range testRunners {
		if r.re.MatchString(c) {
			return &TestInfo{Runner: r.runner, Targets: testTargets(r.runner, c)}
		}
	}
	return nil
}

var (
	pyTarget = regexp.MustCompile(`(\S+\.py(?:::[\w\[\]\-.:]+)?)`)
	goRun    = regexp.MustCompile(`-run[= ]['"]?([^\s'"]+)`)
	jsTarget = regexp.MustCompile(`(\S+\.(?:test|spec)\.[cm]?[jt]sx?)`)
	unitTgt  = regexp.MustCompile(`unittest\s+(?:-\S+\s+)*([\w.]+)`)
)

func testTargets(runner, cmd string) []string {
	var t []string
	switch runner {
	case "pytest":
		for _, m := range pyTarget.FindAllStringSubmatch(cmd, -1) {
			t = append(t, m[1])
		}
		if k := regexp.MustCompile(`-k\s+['"]?([\w\s]+?)['"]?(?:\s|$)`).FindStringSubmatch(cmd); k != nil {
			t = append(t, "-k "+k[1])
		}
	case "unittest":
		if m := unitTgt.FindStringSubmatch(cmd); m != nil && m[1] != "discover" {
			t = append(t, m[1])
		}
	case "go":
		if m := goRun.FindStringSubmatch(cmd); m != nil {
			t = append(t, m[1])
		}
	case "jest", "vitest", "npm", "bun", "mocha":
		for _, m := range jsTarget.FindAllStringSubmatch(cmd, -1) {
			t = append(t, m[1])
		}
	}
	return t
}

// TestOutcome is the parsed result of a test run.
type TestOutcome struct {
	Status  string `json:"status"` // pass | fail | error
	Passed  int    `json:"passed"`
	Failed  int    `json:"failed"`
	Skipped int    `json:"skipped,omitempty"`
}

var (
	pytestSummary = regexp.MustCompile(`(?m)(\d+) (passed|failed|error|errors|skipped|xfailed|xpassed)`)
	unittestRan   = regexp.MustCompile(`Ran (\d+) tests?`)
	unittestFail  = regexp.MustCompile(`FAILED \((?:failures=(\d+))?(?:, )?(?:errors=(\d+))?`)
	jestSummary   = regexp.MustCompile(`Tests:\s+(?:(\d+) failed, )?(?:(\d+) skipped, )?(?:(\d+) passed, )?(\d+) total`)
	cargoSummary  = regexp.MustCompile(`test result: (\w+)\. (\d+) passed; (\d+) failed`)
	goFail        = regexp.MustCompile(`(?m)^(?:--- FAIL|FAIL\s)`)
	goOK          = regexp.MustCompile(`(?m)^ok\s+\S+`)
)

// ParseTestOutcome infers pass/fail counts from test output and exit code.
func ParseTestOutcome(runner, output string, exitCode int) TestOutcome {
	o := TestOutcome{Status: "pass"}
	if exitCode != 0 {
		o.Status = "fail"
	}
	switch {
	case cargoSummary.MatchString(output):
		for _, m := range cargoSummary.FindAllStringSubmatch(output, -1) {
			p, _ := strconv.Atoi(m[2])
			f, _ := strconv.Atoi(m[3])
			o.Passed += p
			o.Failed += f
		}
	case jestSummary.MatchString(output):
		m := jestSummary.FindStringSubmatch(output)
		o.Failed, _ = strconv.Atoi(m[1])
		o.Skipped, _ = strconv.Atoi(m[2])
		o.Passed, _ = strconv.Atoi(m[3])
	case unittestRan.MatchString(output):
		total, _ := strconv.Atoi(unittestRan.FindStringSubmatch(output)[1])
		if m := unittestFail.FindStringSubmatch(output); m != nil {
			f, _ := strconv.Atoi(m[1])
			e, _ := strconv.Atoi(m[2])
			o.Failed = f + e
		}
		o.Passed = total - o.Failed
	case runner == "go":
		o.Failed = len(goFail.FindAllString(output, -1))
		o.Passed = len(goOK.FindAllString(output, -1))
	default:
		for _, m := range pytestSummary.FindAllStringSubmatch(output, -1) {
			n, _ := strconv.Atoi(m[1])
			switch m[2] {
			case "passed", "xpassed":
				o.Passed += n
			case "failed", "error", "errors":
				o.Failed += n
			case "skipped", "xfailed":
				o.Skipped += n
			}
		}
	}
	if o.Failed > 0 && exitCode == 0 {
		o.Status = "fail"
	}
	if exitCode != 0 && o.Failed == 0 && o.Passed == 0 {
		o.Status = "error"
	}
	return o
}

var (
	gitCommitCmd = regexp.MustCompile(`(?:^|[\s;&|(])git\s+(?:-C\s+\S+\s+)?(?:-c\s+\S+\s+)*commit(?:\s|$)`)
	commitOut    = regexp.MustCompile(`(?m)^\[([^\]\s]+)(?: \(root-commit\))? ([0-9a-f]{7,40})\] (.+)$`)
	gitOp        = regexp.MustCompile(`(?:^|[\s;&|(])git\s+(?:-C\s+\S+\s+)?(revert|reset|rebase|cherry-pick|merge|push|stash|checkout|restore|switch|commit|am)\b`)
)

// CommitInfo is a commit detected in command output.
type CommitInfo struct {
	SHA     string `json:"sha"`
	Branch  string `json:"branch"`
	Subject string `json:"subject"`
}

// DetectCommit finds a commit created by a `git commit` command.
func DetectCommit(cmd, output string) *CommitInfo {
	if !gitCommitCmd.MatchString(cmd) && !strings.Contains(cmd, "git commit") {
		return nil
	}
	m := commitOut.FindStringSubmatch(output)
	if m == nil {
		return nil
	}
	return &CommitInfo{Branch: m[1], SHA: m[2], Subject: strings.TrimSpace(m[3])}
}

// GitOps lists git subcommands a shell command runs.
func GitOps(cmd string) []string {
	var ops []string
	for _, m := range gitOp.FindAllStringSubmatch(cmd, -1) {
		ops = append(ops, m[1])
	}
	return ops
}

var exitCodeRe = regexp.MustCompile(`(?m)^(?:Exit code:?|exit code:?|Process exited with code)\s*(-?\d+)`)

// ExitCodeFromText extracts "Exit code N" style markers from tool output.
func ExitCodeFromText(s string) (int, bool) {
	m := exitCodeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// LineHash hashes a normalized source line for provenance matching.
func LineHash(line string) string {
	l := strings.TrimSpace(line)
	if len(l) < 4 {
		return ""
	}
	sum := sha256.Sum256([]byte(l))
	return hex.EncodeToString(sum[:8])
}

// LineHashes hashes non-trivial lines of a text block.
func LineHashes(text string) []string {
	var hs []string
	seen := map[string]bool{}
	for _, l := range strings.Split(text, "\n") {
		h := LineHash(l)
		if h != "" && !seen[h] {
			seen[h] = true
			hs = append(hs, h)
		}
	}
	return hs
}

// IsTestPath reports whether a file path looks like a test file.
func IsTestPath(p string) bool {
	l := strings.ToLower(p)
	base := l
	if i := strings.LastIndex(l, "/"); i >= 0 {
		base = l[i+1:]
	}
	return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, "_test.py") ||
		strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.Contains(l, "/tests/") ||
		strings.HasPrefix(l, "tests/") || strings.Contains(l, "/__tests__/") || strings.HasPrefix(l, "test/") || strings.Contains(l, "/test/")
}

// IsDocPath reports whether a file path looks like documentation.
func IsDocPath(p string) bool {
	l := strings.ToLower(p)
	return strings.HasSuffix(l, ".md") || strings.HasSuffix(l, ".mdx") || strings.HasSuffix(l, ".rst") ||
		strings.HasSuffix(l, ".txt") || strings.HasPrefix(l, "docs/") || strings.Contains(l, "/docs/")
}
