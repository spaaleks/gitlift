package fuzzy

import (
	"sort"
	"strings"
	"unicode"
)

const (
	contiguousBonus = 8
	boundaryBonus   = 6
	gapPenalty      = 3
)

func isBoundary(r rune) bool {
	switch r {
	case '/', '_', '.', ':', '-':
		return true
	}
	return unicode.IsSpace(r)
}

func scoreTerm(term, haystack []rune) (int, bool) {
	score := 0
	index := -1
	previous := -1

	for _, want := range term {
		found := -1
		for i := index + 1; i < len(haystack); i++ {
			if haystack[i] == want {
				found = i
				break
			}
		}
		if found == -1 {
			return 0, false
		}
		index = found

		if previous != -1 && index == previous+1 {
			score += contiguousBonus
		}
		if index == 0 || isBoundary(haystack[index-1]) {
			score += boundaryBonus
		}

		gap := index - previous - 1
		if previous == -1 {
			gap = index
		}
		score -= gap * gapPenalty
		previous = index
	}

	return score, true
}

func Score(query, text string) (int, bool) {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return 0, true
	}

	haystack := []rune(strings.ToLower(text))
	total := 0

	for _, term := range terms {
		score, ok := scoreTerm([]rune(term), haystack)
		if !ok {
			return 0, false
		}
		total += score
	}

	return total*100 - len(haystack), true
}

func Filter(query string, items []string) []int {
	type scored struct {
		index int
		score int
	}

	var matches []scored
	for i, item := range items {
		if score, ok := Score(query, item); ok {
			matches = append(matches, scored{index: i, score: score})
		}
	}

	sort.SliceStable(matches, func(a, b int) bool {
		return matches[a].score > matches[b].score
	})

	out := make([]int, len(matches))
	for i, match := range matches {
		out[i] = match.index
	}
	return out
}
