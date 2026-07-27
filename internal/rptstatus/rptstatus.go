// Package rptstatus turns app_rpt's own CLI text output ("rpt nodes",
// "rpt lstats", "rpt stats", "rpt show variables") into structured data.
// It has no HTTP or html/template dependency, so it's shared by both of
// this app's HTTP-facing layers: internal/server's html rendering and
// internal/cloudagent's JSON relay (see that package's doc comment).
//
// Column positions for tabular output are derived from the output's own
// "----" separator row rather than hardcoded, and headers are taken
// verbatim from the output's header row rather than being named here.
// That matters: this app has been wrong before by assuming a real-world
// Asterisk/HamVoIP format matched what a sample file suggested. Deriving
// the shape from each response means a different app_rpt version with
// different or extra columns still renders correctly, and anything
// unparseable falls back to showing the raw text rather than silently
// displaying a confidently wrong table.
package rptstatus

import (
	"regexp"
	"strings"
)

// isSeparatorLine reports whether a line is a column-rule row, i.e.
// made up only of dashes and spaces, e.g. "----  ----------  ---------".
func isSeparatorLine(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" || !strings.Contains(t, "-") {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] != '-' && t[i] != ' ' {
			return false
		}
	}
	return true
}

// columnStarts returns the index where each run of dashes begins, which
// is where each fixed-width column starts.
func columnStarts(sep string) []int {
	var starts []int
	inRun := false
	for i := 0; i < len(sep); i++ {
		if sep[i] == '-' {
			if !inRun {
				starts = append(starts, i)
				inRun = true
			}
			continue
		}
		inRun = false
	}
	return starts
}

// sliceColumns cuts one output line into fields at the given column
// starts. Each field runs to the start of the next column (so a value
// wider than its own dash run isn't truncated) and the last runs to end
// of line. Lines shorter than a column start yield an empty field
// rather than panicking, since trailing columns are often absent.
func sliceColumns(line string, starts []int) []string {
	out := make([]string, len(starts))
	for i, start := range starts {
		if start >= len(line) {
			continue
		}
		end := len(line)
		if i+1 < len(starts) && starts[i+1] < end {
			end = starts[i+1]
		}
		out[i] = strings.TrimSpace(line[start:end])
	}
	return out
}

// ParseLstats turns "rpt lstats" output into headers plus one row per
// connected peer. ok is false when the output has no recognizable
// header/separator pair, in which case the caller should fall back to
// showing the raw text.
func ParseLstats(out string) (headers []string, rows [][]string, ok bool) {
	lines := strings.Split(out, "\n")
	sep := -1
	for i, l := range lines {
		if isSeparatorLine(l) {
			sep = i
			break
		}
	}
	// sep must be at least 1: the line above it is the header row.
	if sep < 1 {
		return nil, nil, false
	}
	starts := columnStarts(lines[sep])
	if len(starts) == 0 {
		return nil, nil, false
	}
	headers = sliceColumns(lines[sep-1], starts)
	for _, l := range lines[sep+1:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		rows = append(rows, sliceColumns(l, starts))
	}
	return headers, rows, true
}

// StatField is one "Label.........: Value" line from "rpt stats". JSON
// tags exist only for internal/cloudagent's relayed system.nodeStats
// action.
type StatField struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// StatFields wraps the parsed block so a value can be looked up by label
// without re-scanning at each call site.
type StatFields []StatField

// Value returns the value for an exact label, or "" if absent. Labels
// are matched exactly rather than by prefix so a future app_rpt adding
// e.g. "Signal on input B" can't be mistaken for "Signal on input".
func (s StatFields) Value(label string) string {
	for _, f := range s {
		if f.Label == label {
			return f.Value
		}
	}
	return ""
}

// ParseRptStats turns "rpt stats <node>" output into label/value pairs.
// The format is a label padded with dots, a colon, then the value:
//
//	Signal on input..................................: NO
//	TX time today....................................: 00:00:00.0
//
// Splitting at the first colon is what makes the time values work —
// they contain colons of their own, but always after the separator. The
// banner line and blank lines are skipped.
//
// ok is false when nothing parsed, so the caller can fall back to
// showing the raw text rather than an empty panel that would read as
// "this node has no status".
func ParseRptStats(out string) (StatFields, bool) {
	var fields StatFields
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.Contains(t, "***") {
			continue
		}
		i := strings.Index(t, ":")
		if i < 0 {
			continue
		}
		label := strings.TrimRight(strings.TrimSpace(t[:i]), ".")
		label = strings.TrimSpace(label)
		value := strings.TrimSpace(t[i+1:])
		if label == "" {
			continue
		}
		fields = append(fields, StatField{Label: label, Value: value})
	}
	return fields, len(fields) > 0
}

// DisplayHeader softens a column heading for display: app_rpt prints
// its headings in all caps ("CONNECT TIME"), which reads as raw machine
// output on a page meant for people who don't think in CLI. Only
// all-caps headings are touched, so a future app_rpt version already
// using mixed case is left exactly as it wrote it. This is presentation
// only — the heading words themselves are still app_rpt's, not this
// app's invention.
func DisplayHeader(h string) string {
	if h == "" || h != strings.ToUpper(h) {
		return h
	}
	lower := strings.ToLower(h)
	return strings.ToUpper(lower[:1]) + lower[1:]
}

// ParseConnectedNodes pulls the node numbers out of "rpt nodes" output,
// which wraps its list in a "**** CONNECTED NODES ****" banner and uses
// the literal "<NONE>" when nothing is connected. Returns an empty slice
// for the not-connected case, so "no connections" and "couldn't read"
// stay distinguishable to the caller.
func ParseConnectedNodes(out string) []string {
	var nodes []string
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.Contains(t, "***") {
			continue
		}
		for _, f := range strings.FieldsFunc(t, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t'
		}) {
			if f == "" || strings.EqualFold(f, "<NONE>") {
				continue
			}
			nodes = append(nodes, f)
		}
	}
	return nodes
}

// rptAlinksRe extracts the RPT_ALINKS value from "rpt show variables"
// output. It's tolerant of how the command frames the variable (=, :, or
// whitespace) because the value itself contains no spaces, so it can't
// over-capture.
var rptAlinksRe = regexp.MustCompile(`RPT_ALINKS[=:\s]+(\S+)`)

// alinkEntryRe matches one adjacent-node entry: digits, a mode letter,
// and a keyed letter, e.g. "2001RK". Anything not of this exact shape is
// skipped rather than guessed at.
var alinkEntryRe = regexp.MustCompile(`^([0-9]+)([A-Za-z])([A-Za-z])$`)

// KeyedNodes returns the set of adjacent node numbers currently keyed
// (transmitting), read from app_rpt's RPT_ALINKS channel variable in
// "rpt show variables" output.
//
// RPT_ALINKS is documented as:
//
//	<count>,<node><mode><rxkeyed>[,<node><mode><rxkeyed>...]
//
// e.g. "2,2000TU,2001RK" — two adjacent nodes, 2000 in Transceive mode
// and unkeyed (U), 2001 in Receive-only mode and keyed (K). The trailing
// letter is what answers "who's talking".
//
// This is built to fail closed. The value grammar is matched strictly,
// and output with no recognizable RPT_ALINKS yields an empty set — so on
// an app_rpt build that doesn't expose this variable (or where "rpt show
// variables" doesn't exist at all, which is unverified on the HamVoIP
// 0.327 build this was written against), the UI simply shows no talking
// markers rather than anything incorrect.
func KeyedNodes(out string) map[string]bool {
	set := map[string]bool{}
	m := rptAlinksRe.FindStringSubmatch(out)
	if m == nil {
		return set
	}
	parts := strings.Split(m[1], ",")
	// parts[0] is the adjacent-node count; the entries follow it.
	for _, p := range parts[1:] {
		g := alinkEntryRe.FindStringSubmatch(p)
		if g == nil {
			continue
		}
		if strings.EqualFold(g[3], "K") {
			set[g[1]] = true
		}
	}
	return set
}

// NodeReceiving reports whether someone is keying this node's receiver
// right now, from "rpt stats"'s "Signal on input" field.
//
// This is the local RF input specifically. Audio arriving from a linked
// node over the internet does not set it, and this app_rpt build's CLI
// exposes no per-link keyed state at all — "rpt lstats" here reports
// connection state and timers, not who is talking. So this answers "is
// someone transmitting into this node" and deliberately does not claim
// to answer "which connected station is currently talking", which the
// available data cannot support.
func NodeReceiving(fields StatFields) bool {
	return strings.EqualFold(fields.Value("Signal on input"), "YES")
}
