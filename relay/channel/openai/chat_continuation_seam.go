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
)

// seamDeduper strips a model-repeated prefix from one resumed round.
type seamDeduper struct {
	tail    []rune
	pending []rune
	decided bool
}

// newSeamDeduper returns a deduper for a response that already delivered
// deliveredText of visible text, or nil when there is nothing to compare against
// (a thinking-only resume, or a first round that produced no text).
func newSeamDeduper(deliveredText string) *seamDeduper {
	if deliveredText == "" {
		return nil
	}
	return &seamDeduper{tail: []rune(deliveredText)}
}

// feed accepts the next content delta of the resumed round. ready reports whether
// text must be forwarded now; when it is false the delta is held.
func (d *seamDeduper) feed(delta string) (text string, ready bool) {
	if d == nil || d.decided || delta == "" {
		return delta, true
	}
	d.pending = append(d.pending, []rune(delta)...)
	if len(d.pending) < seamMaxLookahead {
		return "", false
	}
	return d.decide(), true
}

// finish flushes whatever is still held when the round ends before the lookahead
// filled up.
func (d *seamDeduper) finish() (text string, ready bool) {
	if d == nil || d.decided {
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
