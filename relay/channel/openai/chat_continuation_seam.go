package openai

// Seam de-duplication for transparent continuation.
//
// A continuation round asks the model to resume from the assistant prefix, and
// the instruction says not to repeat it. Models usually obey, but nothing in the
// protocol forces them to: when one does repeat the tail of the delivered answer,
// the client would see the same sentence twice and the response would stop being
// indistinguishable from a single generation. The gateway therefore compares the
// head of the resumed round with the tail of what it already delivered and drops
// a repeated overlap before forwarding.
//
// The comparison needs the round's first characters, which are not available
// until they arrive. The deduper holds a bounded prefix (seamMaxLookahead) of
// the resumed round, decides once there is enough text or the round ends,
// forwards everything after the overlap, and stops interfering after that
// decision. Holding at most a few hundred characters costs nothing measurable on
// a stream that already runs for seconds.
//
// Overlaps shorter than seamMinOverlap are kept: "the ", "1、2、" and other short
// runs are legitimate continuations, and stripping them would corrupt answers far
// more often than it would help. Lengths are counted in runes, not bytes, so a
// Chinese answer gets the same protection as an English one.
const (
	seamMinOverlap   = 8
	seamMaxLookahead = 256
	// seamMaxDeliveredRunes bounds the delivered answer kept for restart
	// detection: a rotated channel may regenerate the answer from its beginning
	// instead of continuing it, and dropping only a tail overlap would then send
	// the whole prefix twice.
	seamMaxDeliveredRunes = 64 * 1024
)

// seamDeduper strips a model-repeated prefix from one resumed round.
type seamDeduper struct {
	// delivered keeps the answer the client already has (bounded), so a round
	// that regenerates it from the start can be recognised and dropped.
	delivered []rune
	pending   []rune
	tail      []rune
	// While skipping, incoming runes are compared with ref starting at refPos and
	// dropped until they diverge. A repeated prefix longer than the lookahead
	// window is handled this way instead of being truncated at the window edge.
	ref      []rune
	refPos   int
	skipping bool
	decided  bool
}

// newSeamDeduper returns a deduper for a response that already delivered
// deliveredText of visible text, or nil when there is nothing to compare against
// (a thinking-only resume, or a first round that produced no text).
func newSeamDeduper(deliveredText string) *seamDeduper {
	if deliveredText == "" {
		return nil
	}
	delivered := []rune(deliveredText)
	if len(delivered) > seamMaxDeliveredRunes {
		delivered = delivered[:seamMaxDeliveredRunes]
	}
	return &seamDeduper{delivered: delivered, tail: []rune(deliveredText)}
}

// feed accepts the next content delta of the resumed round. ready reports whether
// text must be forwarded now; when it is false the delta is held.
func (d *seamDeduper) feed(delta string) (text string, ready bool) {
	if d == nil || delta == "" {
		return delta, true
	}
	if d.skipping {
		return d.skipRepeat(delta), true
	}
	if d.decided {
		return delta, true
	}
	d.pending = append(d.pending, []rune(delta)...)
	if len(d.pending) < seamMaxLookahead {
		return "", false
	}
	// A round that regenerated the answer from its very beginning looks exactly
	// like the delivered text for the first lookahead runes. Drop the delivered
	// prefix instead of forwarding it a second time.
	if len(d.delivered) >= seamMaxLookahead && runesEqual(d.pending[:seamMaxLookahead], d.delivered[:seamMaxLookahead]) {
		leftover := string(d.pending[seamMaxLookahead:])
		d.pending = nil
		d.skipping = true
		d.ref = d.delivered
		d.refPos = seamMaxLookahead
		if leftover == "" {
			return "", true
		}
		return d.skipRepeat(leftover), true
	}
	return d.decide(), true
}

// skipRepeat drops runes while they keep matching the reference prefix the model
// is repeating, and forwards everything from the first divergence on.
func (d *seamDeduper) skipRepeat(delta string) string {
	runes := []rune(delta)
	i := 0
	for i < len(runes) && d.refPos < len(d.ref) && runes[i] == d.ref[d.refPos] {
		i++
		d.refPos++
	}
	if i == len(runes) && d.refPos < len(d.ref) {
		return ""
	}
	d.skipping = false
	d.decided = true
	rest := string(runes[i:])
	if rest != "" {
		d.tail = append(d.tail, []rune(rest)...)
		if len(d.tail) > 4*seamMaxLookahead {
			d.tail = append([]rune(nil), d.tail[len(d.tail)-4*seamMaxLookahead:]...)
		}
	}
	return rest
}

// finish flushes whatever is still held when the round ends before the lookahead
// filled up.
func (d *seamDeduper) finish() (text string, ready bool) {
	if d == nil || d.decided || d.skipping {
		return "", false
	}
	return d.decide(), true
}

// decide performs the overlap comparison once and marks the deduper finished.
func (d *seamDeduper) decide() string {
	d.decided = true
	pending := d.pending
	d.pending = nil
	if len(pending) == 0 {
		return ""
	}
	maximal := len(pending)
	if maximal > seamMaxLookahead {
		maximal = seamMaxLookahead
	}
	if maximal > len(d.tail) {
		maximal = len(d.tail)
	}
	overlap := 0
	for k := maximal; k >= seamMinOverlap; k-- {
		if runesHaveSuffix(d.tail, pending[:k]) {
			overlap = k
			break
		}
	}
	if overlap == seamMaxLookahead && len(pending) == seamMaxLookahead {
		// The repeat is at least as long as the window: keep comparing with the
		// delivered tail instead of forwarding the rest of the repeated prefix.
		d.skipping = true
		d.ref = d.tail
		d.refPos = len(d.tail) - overlap
		return ""
	}
	out := pending[overlap:]
	if len(out) > 0 {
		// Keep the delivered tail exact so a later round is compared with what the
		// client really has; bound it so a long answer cannot grow the deduper.
		d.tail = append(d.tail, out...)
		if len(d.tail) > 4*seamMaxLookahead {
			d.tail = append([]rune(nil), d.tail[len(d.tail)-4*seamMaxLookahead:]...)
		}
	}
	return string(out)
}

// runesEqual reports whether both rune slices are identical.
func runesEqual(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// runesHaveSuffix reports whether suffix is a suffix of text.
func runesHaveSuffix(text, suffix []rune) bool {
	if len(suffix) > len(text) {
		return false
	}
	start := len(text) - len(suffix)
	for i := range suffix {
		if text[start+i] != suffix[i] {
			return false
		}
	}
	return true
}
