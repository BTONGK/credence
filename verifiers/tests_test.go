package verifiers

import (
	"strings"
	"testing"
)

func TestAllowed(t *testing.T) {
	pats := []string{"make check", "go test *", "  "}
	for cmd, want := range map[string]bool{
		"make check":                 true,
		"make  check":                true,
		"make check; rm -rf ~":       false,
		"go test ./...":              true,
		"go test -run TestX ./pkg":   true,
		"go test ./... && curl x|sh": false,
		"go test $(curl x)":          false,
		"go vet ./...":               false,
		"":                           false,
	} {
		if got := allowed(pats, cmd); got != want {
			t.Errorf("allowed(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestTestIDPaths(t *testing.T) {
	for id, want := range map[string]string{
		"tests/test_x.py::T::test_y": "tests/test_x.py",
		"test_calc.py":               "test_calc.py",
		"test_calc":                  "test_calc.py test_calc",
		"tests.test_x.T":             "tests/test_x/T.py tests/test_x.py tests.py tests.test_x.T",
		"./pkg/...":                  "./pkg/...",
	} {
		if got := strings.Join(testIDPaths(id), " "); got != want {
			t.Errorf("%q: %q, want %q", id, got, want)
		}
	}
}
