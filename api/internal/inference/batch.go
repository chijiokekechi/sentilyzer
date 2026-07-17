package inference

import "sort"

// defaultMaxBatch mirrors the ML worker's SENTILYZER_ML_MAX_BATCH default
// (ml/sentilyzer_ml/config.py). The worker aborts with RESOURCE_EXHAUSTED
// above this rather than chunking on its own, so the client must stay under
// it. Raising it here without raising it on the worker fails every request.
const defaultMaxBatch = 32

// aspectBatchMultiple mirrors the worker's `max_batch_size * 4` ceiling on
// the *cumulative* aspect count of one ClassifyAspects request — the limit
// there counts aspects across all inputs, not inputs.
const aspectBatchMultiple = 4

// lengthOrder returns the indices of texts ordered by ascending length, so
// that chunks drawn from it hold similarly-sized texts.
//
// This is worth more than it looks. The worker tokenizes with padding=True,
// which pads every text in a batch out to the longest one in that batch. A
// single 2000-char document mixed in with short comments makes every text in
// its batch pay the 2000-char cost — measured at ~36x on a 32-text batch.
// Ordering by length confines that cost to the batch that actually earns it.
func lengthOrder(texts []string) []int {
	idx := make([]int, len(texts))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return len(texts[idx[a]]) < len(texts[idx[b]])
	})
	return idx
}

// chunkRanges splits n items into consecutive [start, end) ranges of at most
// max items each. It returns nil when n is 0.
func chunkRanges(n, max int) [][2]int {
	if max < 1 {
		max = 1
	}
	var out [][2]int
	for start := 0; start < n; start += max {
		out = append(out, [2]int{start, min(start+max, n)})
	}
	return out
}

// aspectRef locates one (document, aspect) pair in the caller's input.
type aspectRef struct {
	item int // index into the caller's []AspectInput
	pos  int // index into that item's Aspects
}

// aspectRefs flattens items into one ref per (document, aspect) pair, with
// documents visited in length order and each document's aspects kept
// contiguous. Contiguity is what lets the caller regroup a chunk back into
// per-document requests with a single pass.
//
// Flattening to pairs (rather than chunking whole documents) is what makes
// this total: the worker's ceiling counts aspects, so a single document
// carrying more aspects than the ceiling could never be sent whole. Split
// across chunks, it goes out as the same text with different aspect subsets
// and reassembles correctly.
func aspectRefs(items []AspectInput) []aspectRef {
	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = it.Text
	}
	var refs []aspectRef
	for _, i := range lengthOrder(texts) {
		for p := range items[i].Aspects {
			refs = append(refs, aspectRef{item: i, pos: p})
		}
	}
	return refs
}

// groupByItem collapses a run of refs into per-document groups, preserving
// order. Refs for the same document are adjacent (see aspectRefs), so one
// pass suffices.
func groupByItem(refs []aspectRef) [][]aspectRef {
	var groups [][]aspectRef
	for _, r := range refs {
		if n := len(groups); n > 0 && groups[n-1][0].item == r.item {
			groups[n-1] = append(groups[n-1], r)
			continue
		}
		groups = append(groups, []aspectRef{r})
	}
	return groups
}
