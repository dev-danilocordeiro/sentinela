// Package evals scores the Judge against a hand-labeled dataset of findings.
//
// Each case is a finding (as the Judge sees it) plus a label: accepted (a reviewer would keep the
// issue) or rejected (a reviewer would close it). The runner asks the Judge about every case,
// applies the same decision rule the discovery pipeline uses (discovery.Decide, with re-votes
// near the threshold) and reports agreement per category.
package evals

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"

	"github.com/dev-danilocordeiro/sentinela/internal/agents/judge"
)

// Labels.
const (
	Accepted = "accepted"
	Rejected = "rejected"
)

// Categories in report order.
var Categories = []string{"bug", "performance", "improvement", "security"}

// Case is one labeled finding.
type Case struct {
	ID        string        `json:"id"`
	Label     string        `json:"label"`
	Category  string        `json:"category"`
	Rationale string        `json:"rationale"`
	Finding   judge.Finding `json:"finding"`
}

// Dataset is the loaded file plus a hash of its bytes, so a report can say which version it scored.
type Dataset struct {
	Path   string
	SHA256 string
	Cases  []Case
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Load reads a JSONL dataset and validates every case.
func Load(path string) (Dataset, error) {
	data, err := os.ReadFile(path) //nolint:gosec // dataset path comes from the operator
	if err != nil {
		return Dataset{}, err
	}
	sum := sha256.Sum256(data)
	ds := Dataset{Path: path, SHA256: hex.EncodeToString(sum[:])}
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var errs []error
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var c Case
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			errs = append(errs, fmt.Errorf("line %d: %w", n, err))
			continue
		}
		if err := c.validate(); err != nil {
			errs = append(errs, fmt.Errorf("line %d (%s): %w", n, c.ID, err))
			continue
		}
		if seen[c.ID] {
			errs = append(errs, fmt.Errorf("line %d: duplicate id %q", n, c.ID))
			continue
		}
		seen[c.ID] = true
		ds.Cases = append(ds.Cases, c)
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) == 0 && len(ds.Cases) == 0 {
		errs = append(errs, errors.New("dataset is empty"))
	}
	return ds, errors.Join(errs...)
}

func (c Case) validate() error {
	switch {
	case !idPattern.MatchString(c.ID):
		return fmt.Errorf("id must match %s", idPattern)
	case c.Label != Accepted && c.Label != Rejected:
		return fmt.Errorf("label must be %q or %q, got %q", Accepted, Rejected, c.Label)
	case !knownCategory(c.Category):
		return fmt.Errorf("unknown category %q", c.Category)
	case c.Finding.Category != c.Category:
		return fmt.Errorf("finding.category %q differs from category %q", c.Finding.Category, c.Category)
	case c.Rationale == "":
		return errors.New("rationale is required")
	case c.Finding.Title == "" || len(c.Finding.Evidence) == 0:
		return errors.New("finding needs a title and at least one evidence")
	case c.Finding.Fingerprint == "":
		return errors.New("finding.fingerprint is required")
	}
	return nil
}

func knownCategory(c string) bool {
	for _, k := range Categories {
		if k == c {
			return true
		}
	}
	return false
}
