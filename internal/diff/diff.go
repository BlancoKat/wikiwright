// Package diff is a line-oriented LCS diff used by page history.
package diff

import "strings"

// Kind is an edit operation.
type Kind int

const (
	Equal Kind = iota
	Delete
	Insert
)

// Op is one line of a diff.
type Op struct {
	Kind Kind
	Text string
}

// Lines compares two texts and returns a sequence of line operations.
// Very large inputs fall back to a coarse replacement so history pages stay cheap.
func Lines(a, b string) []Op {
	la := split(a)
	lb := split(b)
	if len(la)*len(lb) > 1_500_000 {
		return coarse(la, lb)
	}
	return lcs(la, lb)
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func coarse(a, b []string) []Op {
	var out []Op
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for ; i < n; i++ {
		if a[i] == b[i] {
			out = append(out, Op{Equal, a[i]})
			continue
		}
		break
	}
	for _, line := range a[i:] {
		out = append(out, Op{Delete, line})
	}
	for _, line := range b[i:] {
		out = append(out, Op{Insert, line})
	}
	return out
}

func lcs(a, b []string) []Op {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var out []Op
	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			out = append(out, Op{Equal, a[i]})
			i++
			j++
			continue
		}
		if dp[i+1][j] >= dp[i][j+1] {
			out = append(out, Op{Delete, a[i]})
			i++
		} else {
			out = append(out, Op{Insert, b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, Op{Delete, a[i]})
	}
	for ; j < m; j++ {
		out = append(out, Op{Insert, b[j]})
	}
	return out
}
