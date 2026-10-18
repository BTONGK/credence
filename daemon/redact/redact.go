// Package redact strips secrets and PII from transcript content before it
// is stored or sent to a model. Replacements keep a short keyed hash so the
// same secret maps to the same placeholder without revealing it.
package redact

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"sync"
)

type rule struct {
	kind string
	re   *regexp.Regexp
	// group is the submatch to replace (0 = whole match).
	group int
	check func(string) bool
}

var builtin = []rule{
	{kind: "private_key", re: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)},
	{kind: "anthropic_key", re: regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{20,}`)},
	{kind: "openai_key", re: regexp.MustCompile(`sk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_\-]{32,}`)},
	{kind: "github_token", re: regexp.MustCompile(`(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{60,}`)},
	{kind: "gitlab_token", re: regexp.MustCompile(`glpat-[A-Za-z0-9_\-]{20,}`)},
	{kind: "slack_token", re: regexp.MustCompile(`xox[abposr]-[A-Za-z0-9-]{10,}`)},
	{kind: "slack_webhook", re: regexp.MustCompile(`https://hooks\.slack\.com/services/[A-Za-z0-9/]+`)},
	{kind: "aws_access_key", re: regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16}\b`)},
	{kind: "aws_secret", re: regexp.MustCompile(`(?i)aws_?secret_?access_?key["'\s:=]+([A-Za-z0-9/+=]{40})`), group: 1},
	{kind: "google_api_key", re: regexp.MustCompile(`AIza[0-9A-Za-z_\-]{35}`)},
	{kind: "stripe_key", re: regexp.MustCompile(`(?:sk|rk)_(?:live|test)_[0-9A-Za-z]{20,}`)},
	{kind: "npm_token", re: regexp.MustCompile(`npm_[A-Za-z0-9]{36}`)},
	{kind: "huggingface_token", re: regexp.MustCompile(`hf_[A-Za-z0-9]{34,}`)},
	{kind: "jwt", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`)},
	{kind: "bearer", re: regexp.MustCompile(`(?i)\b(?:authorization:\s*)?bearer\s+([A-Za-z0-9._\-~+/]{20,}=*)`), group: 1},
	{kind: "url_credentials", re: regexp.MustCompile(`\b([a-z][a-z0-9+.\-]*://[^\s:/@]+:)([^\s@/]{3,})(@)`), group: 2},
	{kind: "assigned_secret", re: regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret|private[_-]?key)\b["']?\s*[:=]\s*["']?([^\s"'<>{}\[\],;]{8,})`), group: 1, check: notPlaceholder},
	{kind: "email", re: regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`), check: notExampleEmail},
	{kind: "credit_card", re: regexp.MustCompile(`\b(?:\d[ -]?){13,19}\b`), check: luhn},
	{kind: "us_ssn", re: regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)},
}

// Redactor applies built-in and repo-specific rules.
type Redactor struct {
	rules []rule
	key   []byte
	mu    sync.Mutex
	// Counts tallies redactions by kind.
	Counts map[string]int
}

// New builds a redactor. key salts placeholder hashes; extra are repo patterns.
func New(key []byte, extra []string) *Redactor {
	r := &Redactor{key: key, Counts: map[string]int{}}
	r.rules = append(r.rules, builtin...)
	for _, p := range extra {
		if re, err := regexp.Compile(p); err == nil {
			r.rules = append(r.rules, rule{kind: "custom", re: re})
		}
	}
	return r
}

func (r *Redactor) placeholder(kind, secret string) string {
	m := hmac.New(sha256.New, r.key)
	m.Write([]byte(secret))
	return "[REDACTED:" + kind + ":" + hex.EncodeToString(m.Sum(nil))[:8] + "]"
}

// String redacts a string.
func (r *Redactor) String(s string) string {
	if s == "" {
		return s
	}
	for _, ru := range r.rules {
		if !ru.re.MatchString(s) {
			continue
		}
		s = replaceRule(s, ru, r)
	}
	s = r.highEntropy(s)
	return s
}

func replaceRule(s string, ru rule, r *Redactor) string {
	idxs := ru.re.FindAllStringSubmatchIndex(s, -1)
	if len(idxs) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range idxs {
		g := ru.group
		start, end := m[2*g], m[2*g+1]
		if start < 0 {
			continue
		}
		secret := s[start:end]
		if strings.HasPrefix(secret, "[REDACTED:") {
			continue
		}
		if ru.check != nil && !ru.check(secret) {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(r.placeholder(ru.kind, secret))
		last = end
		r.mu.Lock()
		r.Counts[ru.kind]++
		r.mu.Unlock()
	}
	b.WriteString(s[last:])
	return b.String()
}

// keyish matches tokens assigned to secret-looking names, used to gate the
// entropy check so ordinary hashes and ids are left alone.
var keyish = regexp.MustCompile(`(?i)(?:key|token|secret|passw|credential|auth)[A-Za-z_\-]*["']?\s*[:=]\s*["']?([A-Za-z0-9+/=_\-]{24,})`)

func (r *Redactor) highEntropy(s string) string {
	idxs := keyish.FindAllStringSubmatchIndex(s, -1)
	if len(idxs) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range idxs {
		start, end := m[2], m[3]
		tok := s[start:end]
		if strings.HasPrefix(tok, "REDACTED") || entropy(tok) < 3.5 {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(r.placeholder("high_entropy", tok))
		last = end
		r.mu.Lock()
		r.Counts["high_entropy"]++
		r.mu.Unlock()
	}
	b.WriteString(s[last:])
	return b.String()
}

// Value redacts every string inside a decoded JSON value.
func (r *Redactor) Value(v any) any {
	switch t := v.(type) {
	case string:
		return r.String(t)
	case map[string]any:
		for k, x := range t {
			t[k] = r.Value(x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = r.Value(x)
		}
		return t
	}
	return v
}

// JSON redacts a JSON document.
func (r *Redactor) JSON(raw []byte) []byte {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return []byte(r.String(string(raw)))
	}
	out, err := json.Marshal(r.Value(v))
	if err != nil {
		return raw
	}
	return out
}

func entropy(s string) float64 {
	if s == "" {
		return 0
	}
	freq := map[rune]float64{}
	for _, c := range s {
		freq[c]++
	}
	n := float64(len([]rune(s)))
	h := 0.0
	for _, f := range freq {
		p := f / n
		h -= p * math.Log2(p)
	}
	return h
}

func luhn(s string) bool {
	digits := make([]int, 0, 19)
	for _, c := range s {
		if c >= '0' && c <= '9' {
			digits = append(digits, int(c-'0'))
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	// Reject runs of one digit and obvious sequences (timestamps, ids).
	same := true
	for _, d := range digits {
		if d != digits[0] {
			same = false
		}
	}
	if same {
		return false
	}
	sum := 0
	alt := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}

var placeholderWords = []string{"example", "placeholder", "changeme", "your_", "your-", "xxxx", "<", "${", "env.", "os.environ", "process.env", "getenv", "redacted"}

func notPlaceholder(s string) bool {
	l := strings.ToLower(s)
	for _, w := range placeholderWords {
		if strings.Contains(l, w) {
			return false
		}
	}
	return true
}

func notExampleEmail(s string) bool {
	l := strings.ToLower(s)
	for _, d := range []string{"@example.", "@users.noreply.github.com", "noreply@", "@localhost", "@test."} {
		if strings.Contains(l, d) {
			return false
		}
	}
	// Package-style strings like "foo@1.2.3" are excluded by the TLD rule already.
	return true
}
