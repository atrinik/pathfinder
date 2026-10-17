package search

// node is one open-list entry.
type node struct {
	f, g  int32
	state int32
}

// heap is a binary min-heap on f; among equal f it prefers the larger g,
// which expands toward the goal before sideways alternatives.
type heap []node

func (h node) less(o node) bool {
	if h.f != o.f {
		return h.f < o.f
	}
	if h.g != o.g {
		return h.g > o.g
	}
	return h.state < o.state
}

func (h *heap) push(n node) {
	*h = append(*h, n)
	a := *h
	i := len(a) - 1
	for i > 0 {
		parent := (i - 1) / 2
		if !a[i].less(a[parent]) {
			break
		}
		a[i], a[parent] = a[parent], a[i]
		i = parent
	}
}

func (h *heap) pop() node {
	a := *h
	top := a[0]
	last := len(a) - 1
	a[0] = a[last]
	a = a[:last]
	i := 0
	for {
		l, r := 2*i+1, 2*i+2
		smallest := i
		if l < len(a) && a[l].less(a[smallest]) {
			smallest = l
		}
		if r < len(a) && a[r].less(a[smallest]) {
			smallest = r
		}
		if smallest == i {
			break
		}
		a[i], a[smallest] = a[smallest], a[i]
		i = smallest
	}
	*h = a
	return top
}
