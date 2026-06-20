package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Status values for a single assertion.
const (
	StatusPass = "PASS"
	StatusFail = "FAIL"
	StatusSkip = "SKIP"
)

// Case is one assertion outcome (serialized into the per-suite JSON).
type Case struct {
	Name     string `json:"name"`
	Status   string `json:"status"`   // PASS | FAIL | SKIP
	Expected string `json:"expected"` // what should have happened
	Actual   string `json:"actual"`   // what was observed
	Reason   string `json:"reason"`   // failure / skip explanation
}

// Suite is the full result document a case program writes.
type Suite struct {
	Suite     string    `json:"suite"`
	Title     string    `json:"title"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	Cases     []Case    `json:"cases"`
}

// Recorder collects Case results and writes them to $RESULT_DIR/<suite>.json.
type Recorder struct {
	suite string
	title string
	start time.Time
	cases []Case
}

// NewRecorder starts a suite. suite is the file stem (e.g. "02_x1_query").
func NewRecorder(suite, title string) *Recorder {
	fmt.Printf("== %s — %s ==\n", suite, title)
	return &Recorder{suite: suite, title: title, start: time.Now()}
}

// Check records PASS when cond is true, otherwise FAIL with reason=actual.
func (r *Recorder) Check(name, expected string, cond bool, actual string) {
	if cond {
		r.Pass(name, expected, actual)
	} else {
		r.Fail(name, expected, actual, actual)
	}
}

func (r *Recorder) Pass(name, expected, actual string) {
	r.cases = append(r.cases, Case{Name: name, Status: StatusPass, Expected: expected, Actual: actual})
	fmt.Printf("  [PASS] %s\n", name)
}

func (r *Recorder) Fail(name, expected, actual, reason string) {
	r.cases = append(r.cases, Case{Name: name, Status: StatusFail, Expected: expected, Actual: actual, Reason: reason})
	fmt.Printf("  [FAIL] %s -> expected %q, got %q\n", name, expected, actual)
}

func (r *Recorder) Skip(name, expected, reason string) {
	r.cases = append(r.cases, Case{Name: name, Status: StatusSkip, Expected: expected, Reason: reason})
	fmt.Printf("  [SKIP] %s -> %s\n", name, reason)
}

// Failed reports whether any case failed (skips do not count as failures).
func (r *Recorder) Failed() bool {
	for _, c := range r.cases {
		if c.Status == StatusFail {
			return true
		}
	}
	return false
}

// ExitCode returns 1 when any case failed, else 0.
func (r *Recorder) ExitCode() int {
	if r.Failed() {
		return 1
	}
	return 0
}

// Finish writes the suite JSON to $RESULT_DIR (default ".") and prints a summary.
// It calls os.Exit with the suite exit code so each case program self-reports.
func (r *Recorder) Finish() {
	dir := os.Getenv("RESULT_DIR")
	if dir == "" {
		dir = "."
	}
	doc := Suite{Suite: r.suite, Title: r.title, StartedAt: r.start, EndedAt: time.Now(), Cases: r.cases}
	var p, f, s int
	for _, c := range r.cases {
		switch c.Status {
		case StatusPass:
			p++
		case StatusFail:
			f++
		case StatusSkip:
			s++
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "result dir %s: %v\n", dir, err)
	}
	out := filepath.Join(dir, r.suite+".json")
	b, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(out, b, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write result %s: %v\n", out, err)
	}
	fmt.Printf("== %s: %d passed, %d failed, %d skipped -> %s ==\n\n", r.suite, p, f, s, out)
	os.Exit(r.ExitCode())
}
