package fuzzy

import "testing"

func TestScoreRequiresEveryTermToMatch(t *testing.T) {
	if _, ok := Score("abc", "a-b-c"); !ok {
		t.Error("a subsequence should match")
	}
	if _, ok := Score("acb", "a-b-c"); ok {
		t.Error("out-of-order characters should not match")
	}
	if _, ok := Score("foo bar", "foo baz"); ok {
		t.Error("every whitespace-separated term must match")
	}
	if _, ok := Score("", "anything"); !ok {
		t.Error("an empty query matches everything")
	}
}

func TestFilterRanksBoundaryAndContiguousMatchesFirst(t *testing.T) {
	items := []string{"unrelated-gadget", "gitlift", "a/g/i/t", "legit-thing"}

	order := Filter("git", items)
	if len(order) == 0 {
		t.Fatal("expected matches")
	}
	if items[order[0]] != "gitlift" {
		t.Errorf("best match = %q, want gitlift", items[order[0]])
	}
}

func TestFilterIsCaseInsensitiveAndStable(t *testing.T) {
	items := []string{"Alpha", "alpha", "ALPHA"}
	order := Filter("alpha", items)

	if len(order) != 3 {
		t.Fatalf("got %d matches, want 3", len(order))
	}
	for i, index := range order {
		if index != i {
			t.Errorf("equal scores should keep the input order, got %v", order)
			break
		}
	}
}
