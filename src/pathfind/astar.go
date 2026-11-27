package pathfind

import (
	"container/heap"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// nodeRecord tracks traversal state for a voxel coordinate during A* search.
type nodeRecord struct {
	pos    protocol.BlockPos
	g      float64
	h      float64
	f      float64
	index  int // Index in priority queue min-heap (-1 if not in heap)
	parent *nodeRecord
	move   Move
	closed bool
}

// priorityQueue implements heap.Interface for *nodeRecord ordered by F-score.
type priorityQueue []*nodeRecord

func (pq priorityQueue) Len() int           { return len(pq) }
func (pq priorityQueue) Less(i, j int) bool { return pq[i].f < pq[j].f }
func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *priorityQueue) Push(x any) {
	n := len(*pq)
	item := x.(*nodeRecord)
	item.index = n
	*pq = append(*pq, item)
}

func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil // Avoid memory leak
	item.index = -1
	*pq = old[0 : n-1]
	return item
}

// Planner provides 3D voxel A* path calculation.
type Planner struct{}

// NewPlanner initializes an A* path planning engine.
func NewPlanner() *Planner {
	return &Planner{}
}

// Plan calculates an optimal or best-effort 3D path from start to goal.
// Returns PathResult and true if the exact goal was reached, or false if a best-effort
// partial path was returned due to obstacles or search budget limits.
func (p *Planner) Plan(w WorldView, start, goal protocol.BlockPos, opts SearchOptions) (PathResult, bool) {
	if start == goal {
		return PathResult{
			Nodes:       []protocol.BlockPos{start},
			Cost:        0,
			ReachedGoal: true,
		}, true
	}

	opts = opts.normalized()

	deadline := time.Now().Add(opts.Timeout)

	// Records lookup table to track open/closed nodes
	records := make(map[protocol.BlockPos]*nodeRecord, 256)

	startH := opts.Weights.Heuristic(start, goal)
	startNode := &nodeRecord{
		pos:   start,
		g:     0,
		h:     startH,
		f:     startH,
		index: -1,
	}
	records[start] = startNode

	pq := make(priorityQueue, 0, 128)
	heap.Push(&pq, startNode)

	bestNode := startNode
	reachedGoal := false
	expansions := 0

	for pq.Len() > 0 {
		// Check search budget
		if expansions >= opts.MaxNodes || (expansions%64 == 0 && time.Now().After(deadline)) {
			break
		}

		curr := heap.Pop(&pq).(*nodeRecord)
		curr.closed = true
		expansions++

		// Check if goal reached
		if curr.pos == goal {
			bestNode = curr
			reachedGoal = true
			break
		}

		// Track closest node to goal for best-effort fallback
		if curr.h < bestNode.h {
			bestNode = curr
		}

		// Generate and inspect neighbor moves
		neighbors := GetNeighbors(w, curr.pos, opts)
		for _, m := range neighbors {
			nextPos := m.To
			tentativeG := curr.g + m.Cost

			rec, exists := records[nextPos]
			if !exists {
				nextH := opts.Weights.Heuristic(nextPos, goal)
				rec = &nodeRecord{
					pos:    nextPos,
					g:      tentativeG,
					h:      nextH,
					f:      tentativeG + nextH,
					index:  -1,
					parent: curr,
					move:   m,
				}
				records[nextPos] = rec
				heap.Push(&pq, rec)
			} else {
				if rec.closed && tentativeG >= rec.g {
					continue
				}
				if tentativeG < rec.g {
					rec.g = tentativeG
					rec.f = tentativeG + rec.h
					rec.parent = curr
					rec.move = m
					if rec.index >= 0 {
						heap.Fix(&pq, rec.index)
					} else {
						rec.closed = false
						heap.Push(&pq, rec)
					}
				}
			}
		}
	}

	// Reconstruct path backward from bestNode to start
	result := reconstructPath(bestNode, start)
	result.ReachedGoal = reachedGoal
	return result, reachedGoal
}

// reconstructPath walks parent pointers from endNode back to start.
func reconstructPath(endNode *nodeRecord, start protocol.BlockPos) PathResult {
	if endNode == nil || endNode.pos == start {
		return PathResult{
			Nodes: []protocol.BlockPos{start},
			Cost:  0,
		}
	}

	// Count path length
	count := 0
	curr := endNode
	for curr != nil {
		count++
		curr = curr.parent
	}

	nodes := make([]protocol.BlockPos, count)
	moves := make([]Move, count-1)

	curr = endNode
	idx := count - 1
	for curr != nil {
		nodes[idx] = curr.pos
		if curr.parent != nil {
			moves[idx-1] = curr.move
		}
		curr = curr.parent
		idx--
	}

	return PathResult{
		Moves: moves,
		Nodes: nodes,
		Cost:  endNode.g,
	}
}
