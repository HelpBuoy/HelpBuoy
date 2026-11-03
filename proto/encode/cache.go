package encode

import (
	"encoding/binary"
	"hash/maphash"

	"github.com/mevdschee/helpbuoy/proto/screen"
)

// lru is the encoder's view of the helper's tile cache: which content hash
// is in which slot, and the least recently used order for reuse. The helper
// only stores and draws what the ops tell it, so this is the single source
// of truth for both sides.
type lru struct {
	slots map[uint64]uint16
	key   [screen.CacheSlots]uint64
	// Doubly linked list in recency order; index screen.CacheSlots is the
	// sentinel: next[sentinel] is the most, prev[sentinel] the least
	// recently used slot.
	next, prev [screen.CacheSlots + 1]uint16
	used       int // slots 0..used-1 have been assigned at least once
}

const sentinel = screen.CacheSlots

func newLRU() *lru {
	c := &lru{}
	c.reset()
	return c
}

// reset empties the cache, as on the helper after Reset.
func (c *lru) reset() {
	c.slots = make(map[uint64]uint16)
	c.used = 0
	c.next[sentinel], c.prev[sentinel] = sentinel, sentinel
}

func (c *lru) unlink(s uint16) {
	c.next[c.prev[s]] = c.next[s]
	c.prev[c.next[s]] = c.prev[s]
}

func (c *lru) pushFront(s uint16) {
	c.next[s] = c.next[sentinel]
	c.prev[s] = sentinel
	c.prev[c.next[sentinel]] = s
	c.next[sentinel] = s
}

// lookup returns the slot holding content k and marks it recently used.
func (c *lru) lookup(k uint64) (uint16, bool) {
	s, ok := c.slots[k]
	if ok {
		c.unlink(s)
		c.pushFront(s)
	}
	return s, ok
}

// has reports whether content k is cached, without touching the order.
func (c *lru) has(k uint64) bool {
	_, ok := c.slots[k]
	return ok
}

// insert assigns a slot for content k: a never used one while there are
// any, otherwise the least recently used one, whose content is forgotten.
func (c *lru) insert(k uint64) uint16 {
	if s, ok := c.slots[k]; ok {
		c.unlink(s)
		c.pushFront(s)
		return s
	}
	var s uint16
	if c.used < screen.CacheSlots {
		s = uint16(c.used)
		c.used++
	} else {
		s = c.prev[sentinel]
		c.unlink(s)
		delete(c.slots, c.key[s])
	}
	c.key[s] = k
	c.slots[k] = s
	c.pushFront(s)
	return s
}

// hashRect returns the cache key of a w x h RGBA rectangle starting at
// pix[0] with the given stride. The size is part of the key because a cache
// slot can only be drawn at its stored size.
func hashRect(seed maphash.Seed, pix []byte, stride, w, h int) uint64 {
	var mh maphash.Hash
	mh.SetSeed(seed)
	var hdr [4]byte
	binary.LittleEndian.PutUint16(hdr[0:], uint16(w))
	binary.LittleEndian.PutUint16(hdr[2:], uint16(h))
	mh.Write(hdr[:])
	for y := range h {
		mh.Write(pix[y*stride : y*stride+w*4])
	}
	return mh.Sum64()
}
