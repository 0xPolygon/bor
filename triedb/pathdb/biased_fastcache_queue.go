package pathdb

// preloadQueueSegmentSize is the number of items held by one preloadQueue
// segment. With 32-byte items a segment is 128 KiB, so the queue never makes
// an allocation larger than that, no matter how long it grows.
const preloadQueueSegmentSize = 4096

// preloadQueueItem is a pending node in the storage trie preload BFS.
type preloadQueueItem struct {
	path  []byte
	depth int
}

// preloadQueueSegment is a fixed-size block of queued items, linked to the
// next (newer) segment.
type preloadQueueSegment struct {
	items [preloadQueueSegmentSize]preloadQueueItem
	next  *preloadQueueSegment
}

// preloadQueue is an unbounded FIFO used by the storage trie preload BFS.
//
// A plain slice queue (append + queue[1:]) grows by reallocating and copying
// one contiguous array. For large contracts the BFS frontier reaches hundreds
// of millions of items, so each growth step is a multi-GB pointerful
// allocation and copy that cannot be preempted, and can stall the whole
// process if a GC stop-the-world lands during it. It also keeps consumed items
// (and their paths) reachable until the next growth.
//
// preloadQueue instead stores items in a linked list of fixed-size segments.
// Push allocates at most one segment at a time, and a segment is unlinked (and
// can be collected) as soon as all its items are popped. Popped slots are
// cleared so their paths can be collected right away.
type preloadQueue struct {
	head    *preloadQueueSegment // oldest segment, items are popped from here
	tail    *preloadQueueSegment // newest segment, items are pushed here
	headIdx int                  // index of the next item to pop in head
	tailIdx int                  // index of the next free slot in tail
	size    int                  // number of queued items
}

// push appends an item to the back of the queue.
func (q *preloadQueue) push(item preloadQueueItem) {
	if q.tail == nil {
		q.head = new(preloadQueueSegment)
		q.tail = q.head
	} else if q.tailIdx == preloadQueueSegmentSize {
		seg := new(preloadQueueSegment)
		q.tail.next = seg
		q.tail = seg
		q.tailIdx = 0
	}
	q.tail.items[q.tailIdx] = item
	q.tailIdx++
	q.size++
}

// pop removes and returns the item at the front of the queue. The second
// return value is false if the queue is empty.
func (q *preloadQueue) pop() (preloadQueueItem, bool) {
	if q.size == 0 {
		return preloadQueueItem{}, false
	}
	item := q.head.items[q.headIdx]
	q.head.items[q.headIdx] = preloadQueueItem{} // release the path for GC
	q.headIdx++
	q.size--

	if q.head == q.tail {
		// Single segment: once drained, rewind it so it can be reused.
		if q.headIdx == q.tailIdx {
			q.headIdx, q.tailIdx = 0, 0
		}
	} else if q.headIdx == preloadQueueSegmentSize {
		// Head segment fully consumed: drop it so it can be collected.
		next := q.head.next
		q.head.next = nil
		q.head = next
		q.headIdx = 0
	}
	return item, true
}

// len returns the number of queued items.
func (q *preloadQueue) len() int {
	return q.size
}
