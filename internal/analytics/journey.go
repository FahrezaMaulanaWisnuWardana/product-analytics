package analytics

import (
	"sort"
	"strings"
)

type JourneyNode struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Type   string `json:"type"`
	People int    `json:"people"`
}
type JourneyColumn struct {
	People int           `json:"people"`
	Nodes  []JourneyNode `json:"nodes"`
}
type JourneyEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	People int    `json:"people"`
}
type JourneyResult struct {
	Columns  []JourneyColumn `json:"columns"`
	Edges    [][]JourneyEdge `json:"edges"`
	MaxDepth int             `json:"max_depth"`
}

func journeySequences(events []Event) map[string][]string {
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Timestamp.Equal(sorted[j].Timestamp) {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].Timestamp.Before(sorted[j].Timestamp)
	})
	sequences := map[string][]string{}
	for _, e := range sorted {
		key := "event:" + e.Name
		if e.Name == "$pageview" {
			if path, ok := e.Properties["$pathname"].(string); ok && path != "" {
				key = "page:" + path
			}
		}
		seq := sequences[e.DistinctID]
		if len(seq) == 0 || seq[len(seq)-1] != key {
			sequences[e.DistinctID] = append(seq, key)
		}
	}
	return sequences
}

// JourneyMaxDepth returns the longest observed path in the current project
// and query range after consecutive duplicate steps have been collapsed.
func JourneyMaxDepth(events []Event) int {
	maxDepth := 0
	for _, sequence := range journeySequences(events) {
		if len(sequence) > maxDepth {
			maxDepth = len(sequence)
		}
	}
	return maxDepth
}

func sequenceStart(sequence, prefix []string) int {
	if len(prefix) == 0 {
		return 0
	}
	for i := 0; i+len(prefix) <= len(sequence); i++ {
		match := true
		for j, step := range prefix {
			if sequence[i+j] != step {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// Journey counts distinct people following the selected prefix, with each
// person's first matching sequence counted once.
func Journey(events []Event, selected []string, depth, limit int) JourneyResult {
	sequences := journeySequences(events)
	maxDepth := 0
	for _, sequence := range sequences {
		if len(sequence) > maxDepth {
			maxDepth = len(sequence)
		}
	}
	result := JourneyResult{Columns: make([]JourneyColumn, depth), Edges: make([][]JourneyEdge, depth-1), MaxDepth: maxDepth}
	nodes := make([]map[string]int, depth)
	edges := make([]map[string]int, depth-1)
	for i := range nodes {
		nodes[i] = map[string]int{}
	}
	for i := range edges {
		edges[i] = map[string]int{}
	}
	for _, seq := range sequences {
		for i := 0; i < depth; i++ {
			prefixLength := min(i+1, len(selected))
			start := sequenceStart(seq, selected[:prefixLength])
			if start < 0 || start+i >= len(seq) {
				continue
			}
			result.Columns[i].People++
			nodes[i][seq[start+i]]++
			if i > 0 {
				edges[i-1][seq[start+i-1]+"\x00"+seq[start+i]]++
			}
		}
	}
	for i, counts := range nodes {
		for key, n := range counts {
			parts := strings.SplitN(key, ":", 2)
			result.Columns[i].Nodes = append(result.Columns[i].Nodes, JourneyNode{ID: key, Type: parts[0], Label: parts[1], People: n})
		}
		sort.Slice(result.Columns[i].Nodes, func(a, b int) bool {
			x, y := result.Columns[i].Nodes[a], result.Columns[i].Nodes[b]
			if x.People == y.People {
				return x.ID < y.ID
			}
			return x.People > y.People
		})
		if len(result.Columns[i].Nodes) > limit {
			result.Columns[i].Nodes = result.Columns[i].Nodes[:limit]
		}
	}
	for i, counts := range edges {
		for key, n := range counts {
			parts := strings.SplitN(key, "\x00", 2)
			result.Edges[i] = append(result.Edges[i], JourneyEdge{From: parts[0], To: parts[1], People: n})
		}
		sort.Slice(result.Edges[i], func(a, b int) bool { return result.Edges[i][a].People > result.Edges[i][b].People })
	}
	return result
}
