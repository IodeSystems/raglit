package raglit

import "testing"

func TestParseRangeIsInclusive(t *testing.T) {
	for in, want := range map[string][2]int{
		"3-6": {3, 6}, "3": {3, 3}, "p3-6": {3, 6}, " 10 - 12 ": {10, 12},
	} {
		from, to, err := ParseRange(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if from != want[0] || to != want[1] {
			t.Errorf("%q: got %d-%d want %d-%d", in, from, to, want[0], want[1])
		}
	}
	for _, bad := range []string{"", "a-b", "3-", "-4"} {
		if _, _, err := ParseRange(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

// A child path must announce its provenance, and anything checking the
// filesystem must be able to tell it is not a file — reading its absence as
// "the source vanished" would delete documents working exactly as designed.
func TestAChildPathNamesItsBundle(t *testing.T) {
	sl := Slice{ID: "x", Parent: "/a/scan.pdf", From: 3, To: 6}
	cp := sl.ChildPath()
	if cp != "/a/scan.pdf#p3-6" {
		t.Fatalf("got %q", cp)
	}
	if !IsChildPath(cp) {
		t.Error("a child path must be recognisable as one")
	}
	if got := ChildParent(cp); got != "/a/scan.pdf" {
		t.Errorf("parent: got %q", got)
	}
	for _, notChild := range []string{"/a/scan.pdf", "/a/b#notpages", "/a/b#p3", "/a/b#px-y"} {
		if IsChildPath(notChild) {
			t.Errorf("%q is not a child path", notChild)
		}
	}
}

// Coverage is what says a bundle is FULLY linearized rather than merely
// started. Overlap is reported and not refused: a declaration and the exhibit
// bound into it genuinely occupy the same pages.
func TestCoverageNamesTheGapsAndTheOverlaps(t *testing.T) {
	slices := []Slice{
		{ID: "a", Parent: "s.pdf", From: 1, To: 4},
		{ID: "b", Parent: "s.pdf", From: 4, To: 6},
		{ID: "c", Parent: "s.pdf", From: 9, To: 10},
	}
	c := sliceCoverage("s.pdf", 12, slices)
	if c.Covered != 8 {
		t.Errorf("covered: got %d want 8", c.Covered)
	}
	want := []int{7, 8, 11, 12}
	if len(c.Uncovered) != len(want) {
		t.Fatalf("uncovered: got %v want %v", c.Uncovered, want)
	}
	for i, p := range want {
		if c.Uncovered[i] != p {
			t.Errorf("uncovered: got %v want %v", c.Uncovered, want)
			break
		}
	}
	if len(c.Overlaps) != 1 || c.Overlaps[0][0] != 4 {
		t.Errorf("page 4 is in two slices and must be reported: %v", c.Overlaps)
	}
}

// A fully claimed bundle reports no gap — the completion test.
func TestAFullyLinearizedBundleHasNoGap(t *testing.T) {
	c := sliceCoverage("s.pdf", 6, []Slice{
		{ID: "a", Parent: "s.pdf", From: 1, To: 3},
		{ID: "b", Parent: "s.pdf", From: 4, To: 6},
	})
	if len(c.Uncovered) != 0 || c.Covered != 6 {
		t.Errorf("want fully covered, got %+v", c)
	}
}
