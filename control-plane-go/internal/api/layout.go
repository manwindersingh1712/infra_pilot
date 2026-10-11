package api

const (
	nodeWidth  = 180.0
	nodeHeight = 80.0
	nodeSep    = 150.0 // vertical gap between nodes in one rank
	rankSep    = 200.0 // horizontal gap between ranks
)

type position struct {
	ID        string  `json:"id"`
	PositionX float64 `json:"positionX"`
	PositionY float64 `json:"positionY"`
}

// layout places nodes left to right. A node's rank is one more than its
// furthest predecessor's (longest path); nodes in a rank stack top to bottom
// in input order. Ranks are capped below len(ids), so cycles terminate. Edges
// to unknown nodes and self-loops are ignored.
func layout(ids []string, edges [][2]string) []position {
	known := make(map[string]bool, len(ids))
	for _, id := range ids {
		known[id] = true
	}
	rank := map[string]int{}
	for range ids {
		changed := false
		for _, e := range edges {
			from, to := e[0], e[1]
			if !known[from] || !known[to] || from == to {
				continue
			}
			if next := rank[from] + 1; next > rank[to] && next < len(ids) {
				rank[to] = next
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	slot := map[int]int{}
	out := make([]position, 0, len(ids))
	for _, id := range ids {
		r := rank[id]
		out = append(out, position{
			ID:        id,
			PositionX: float64(r) * (nodeWidth + rankSep),
			PositionY: float64(slot[r]) * (nodeHeight + nodeSep),
		})
		slot[r]++
	}
	return out
}
