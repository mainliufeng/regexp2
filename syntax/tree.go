package syntax

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
)

type RegexTree struct {
	Root       *RegexNode
	caps       map[int]int
	capnumlist []int
	captop     int
	Capnames   map[string]int
	Caplist    []string
	options    RegexOptions
}

// It is built into a parsed tree for a regular expression.

// Implementation notes:
//
// Since the node tree is a temporary data structure only used
// during compilation of the regexp to integer codes, it's
// designed for clarity and convenience rather than
// space efficiency.
//
// RegexNodes are built into a tree, linked by the n.children list.
// Each node also has a n.parent and n.ichild member indicating
// its parent and which child # it is in its parent's list.
//
// RegexNodes come in as many types as there are constructs in
// a regular expression, for example, "concatenate", "alternate",
// "one", "rept", "group". There are also node types for basic
// peephole optimizations, e.g., "onerep", "notsetrep", etc.
//
// Because perl 5 allows "lookback" groups that scan backwards,
// each node also gets a "direction". Normally the value of
// boolean n.backward = false.
//
// During parsing, top-level nodes are also stacked onto a parse
// stack (a stack of trees). For this purpose we have a n.next
// pointer. [Note that to save a few bytes, we could overload the
// n.parent pointer instead.]
//
// On the parse stack, each tree has a "role" - basically, the
// nonterminal in the grammar that the parser has currently
// assigned to the tree. That code is stored in n.role.
//
// Finally, some of the different kinds of nodes have data.
// Two integers (for the looping constructs) are stored in
// n.operands, an an object (either a string or a set)
// is stored in n.data
type RegexNode struct {
	T        NodeType
	Children []*RegexNode
	Str      []rune
	Set      *CharSet
	Ch       rune
	M        int
	N        int
	Options  RegexOptions
	Next     *RegexNode
}

type NodeType int32

const (
	// The following are leaves, and correspond to primitive operations

	NtOnerep      NodeType = 0  // lef,back char,min,max    a {n}
	NtNotonerep            = 1  // lef,back char,min,max    .{n}
	NtSetrep               = 2  // lef,back set,min,max     [\d]{n}
	NtOneloop              = 3  // lef,back char,min,max    a {,n}
	NtNotoneloop           = 4  // lef,back char,min,max    .{,n}
	NtSetloop              = 5  // lef,back set,min,max     [\d]{,n}
	NtOnelazy              = 6  // lef,back char,min,max    a {,n}?
	NtNotonelazy           = 7  // lef,back char,min,max    .{,n}?
	NtSetlazy              = 8  // lef,back set,min,max     [\d]{,n}?
	NtOne                  = 9  // lef      char            a
	NtNotone               = 10 // lef      char            [^a]
	NtSet                  = 11 // lef      set             [a-z\s]  \w \s \d
	NtMulti                = 12 // lef      string          abcd
	NtRef                  = 13 // lef      group           \#
	NtBol                  = 14 //                          ^
	NtEol                  = 15 //                          $
	NtBoundary             = 16 //                          \b
	NtNonboundary          = 17 //                          \B
	NtBeginning            = 18 //                          \A
	NtStart                = 19 //                          \G
	NtEndZ                 = 20 //                          \Z
	NtEnd                  = 21 //                          \Z

	// Interior nodes do not correspond to primitive operations, but
	// control structures compositing other operations

	// Concat and alternate take n children, and can run forward or backwards

	NtNothing     = 22 //          []
	NtEmpty       = 23 //          ()
	NtAlternate   = 24 //          a|b
	NtConcatenate = 25 //          ab
	NtLoop        = 26 // m,x      * + ? {,}
	NtLazyloop    = 27 // m,x      *? +? ?? {,}?
	NtCapture     = 28 // n        ()
	NtGroup       = 29 //          (?:)
	NtRequire     = 30 //          (?=) (?<=)
	NtPrevent     = 31 //          (?!) (?<!)
	NtGreedy      = 32 //          (?>) (?<)
	NtTestref     = 33 //          (?(n) | )
	NtTestgroup   = 34 //          (?(...) | )

	NtECMABoundary    = 41 //                          \b
	NtNonECMABoundary = 42 //                          \B
)

func newRegexNode(t NodeType, opt RegexOptions) *RegexNode {
	return &RegexNode{
		T:       t,
		Options: opt,
	}
}

func newRegexNodeCh(t NodeType, opt RegexOptions, ch rune) *RegexNode {
	return &RegexNode{
		T:       t,
		Options: opt,
		Ch:      ch,
	}
}

func newRegexNodeStr(t NodeType, opt RegexOptions, str []rune) *RegexNode {
	return &RegexNode{
		T:       t,
		Options: opt,
		Str:     str,
	}
}

func newRegexNodeSet(t NodeType, opt RegexOptions, set *CharSet) *RegexNode {
	return &RegexNode{
		T:       t,
		Options: opt,
		Set:     set,
	}
}

func newRegexNodeM(t NodeType, opt RegexOptions, m int) *RegexNode {
	return &RegexNode{
		T:       t,
		Options: opt,
		M:       m,
	}
}
func newRegexNodeMN(t NodeType, opt RegexOptions, m, n int) *RegexNode {
	return &RegexNode{
		T:       t,
		Options: opt,
		M:       m,
		N:       n,
	}
}

func (n *RegexNode) writeStrToBuf(buf *bytes.Buffer) {
	for i := 0; i < len(n.Str); i++ {
		buf.WriteRune(n.Str[i])
	}
}

func (n *RegexNode) addChild(child *RegexNode) {
	reduced := child.reduce()
	n.Children = append(n.Children, reduced)
	reduced.Next = n
}

func (n *RegexNode) insertChildren(afterIndex int, nodes []*RegexNode) {
	newChildren := make([]*RegexNode, 0, len(n.Children)+len(nodes))
	n.Children = append(append(append(newChildren, n.Children[:afterIndex]...), nodes...), n.Children[afterIndex:]...)
}

// removes children including the start but not the end index
func (n *RegexNode) removeChildren(startIndex, endIndex int) {
	n.Children = append(n.Children[:startIndex], n.Children[endIndex:]...)
}

// Pass type as OneLazy or OneLoop
func (n *RegexNode) makeRep(t NodeType, min, max int) {
	n.T += (t - NtOne)
	n.M = min
	n.N = max
}

func (n *RegexNode) reduce() *RegexNode {
	switch n.T {
	case NtAlternate:
		return n.reduceAlternation()

	case NtConcatenate:
		return n.reduceConcatenation()

	case NtLoop, NtLazyloop:
		return n.reduceRep()

	case NtGroup:
		return n.reduceGroup()

	case NtSet, NtSetloop:
		return n.reduceSet()

	default:
		return n
	}
}

// Basic optimization. Single-letter alternations can be replaced
// by faster set specifications, and nested alternations with no
// intervening operators can be flattened:
//
// a|b|c|def|g|h -> [a-c]|def|[gh]
// apple|(?:orange|pear)|grape -> apple|orange|pear|grape
func (n *RegexNode) reduceAlternation() *RegexNode {
	if len(n.Children) == 0 {
		return newRegexNode(NtNothing, n.Options)
	}

	wasLastSet := false
	lastNodeCannotMerge := false
	var optionsLast RegexOptions
	var i, j int

	for i, j = 0, 0; i < len(n.Children); i, j = i+1, j+1 {
		at := n.Children[i]

		if j < i {
			n.Children[j] = at
		}

		for {
			if at.T == NtAlternate {
				for k := 0; k < len(at.Children); k++ {
					at.Children[k].Next = n
				}
				n.insertChildren(i+1, at.Children)

				j--
			} else if at.T == NtSet || at.T == NtOne {
				// Cannot merge sets if L or I options differ, or if either are negated.
				optionsAt := at.Options & (RightToLeft | IgnoreCase)

				if at.T == NtSet {
					if !wasLastSet || optionsLast != optionsAt || lastNodeCannotMerge || !at.Set.IsMergeable() {
						wasLastSet = true
						lastNodeCannotMerge = !at.Set.IsMergeable()
						optionsLast = optionsAt
						break
					}
				} else if !wasLastSet || optionsLast != optionsAt || lastNodeCannotMerge {
					wasLastSet = true
					lastNodeCannotMerge = false
					optionsLast = optionsAt
					break
				}

				// The last node was a Set or a One, we're a Set or One and our options are the same.
				// Merge the two nodes.
				j--
				prev := n.Children[j]

				var prevCharClass *CharSet
				if prev.T == NtOne {
					prevCharClass = &CharSet{}
					prevCharClass.addChar(prev.Ch)
				} else {
					prevCharClass = prev.Set
				}

				if at.T == NtOne {
					prevCharClass.addChar(at.Ch)
				} else {
					prevCharClass.addSet(*at.Set)
				}

				prev.T = NtSet
				prev.Set = prevCharClass
			} else if at.T == NtNothing {
				j--
			} else {
				wasLastSet = false
				lastNodeCannotMerge = false
			}
			break
		}
	}

	if j < i {
		n.removeChildren(j, i)
	}

	return n.stripEnation(NtNothing)
}

// Basic optimization. Adjacent strings can be concatenated.
//
// (?:abc)(?:def) -> abcdef
func (n *RegexNode) reduceConcatenation() *RegexNode {
	// Eliminate empties and concat adjacent strings/chars

	var optionsLast RegexOptions
	var optionsAt RegexOptions
	var i, j int

	if len(n.Children) == 0 {
		return newRegexNode(NtEmpty, n.Options)
	}

	wasLastString := false

	for i, j = 0, 0; i < len(n.Children); i, j = i+1, j+1 {
		var at, prev *RegexNode

		at = n.Children[i]

		if j < i {
			n.Children[j] = at
		}

		if at.T == NtConcatenate &&
			((at.Options & RightToLeft) == (n.Options & RightToLeft)) {
			for k := 0; k < len(at.Children); k++ {
				at.Children[k].Next = n
			}

			//insert at.children at i+1 index in n.children
			n.insertChildren(i+1, at.Children)

			j--
		} else if at.T == NtMulti || at.T == NtOne {
			// Cannot merge strings if L or I options differ
			optionsAt = at.Options & (RightToLeft | IgnoreCase)

			if !wasLastString || optionsLast != optionsAt {
				wasLastString = true
				optionsLast = optionsAt
				continue
			}

			j--
			prev = n.Children[j]

			if prev.T == NtOne {
				prev.T = NtMulti
				prev.Str = []rune{prev.Ch}
			}

			if (optionsAt & RightToLeft) == 0 {
				if at.T == NtOne {
					prev.Str = append(prev.Str, at.Ch)
				} else {
					prev.Str = append(prev.Str, at.Str...)
				}
			} else {
				if at.T == NtOne {
					// insert at the front by expanding our slice, copying the data over, and then setting the value
					prev.Str = append(prev.Str, 0)
					copy(prev.Str[1:], prev.Str)
					prev.Str[0] = at.Ch
				} else {
					//insert at the front...this one we'll make a new slice and copy both into it
					merge := make([]rune, len(prev.Str)+len(at.Str))
					copy(merge, at.Str)
					copy(merge[len(at.Str):], prev.Str)
					prev.Str = merge
				}
			}
		} else if at.T == NtEmpty {
			j--
		} else {
			wasLastString = false
		}
	}

	if j < i {
		// remove indices j through i from the children
		n.removeChildren(j, i)
	}

	return n.stripEnation(NtEmpty)
}

// Nested repeaters just get multiplied with each other if they're not
// too lumpy
func (n *RegexNode) reduceRep() *RegexNode {

	u := n
	t := n.T
	min := n.M
	max := n.N

	for {
		if len(u.Children) == 0 {
			break
		}

		child := u.Children[0]

		// multiply reps of the same type only
		if child.T != t {
			childType := child.T

			if !(childType >= NtOneloop && childType <= NtSetloop && t == NtLoop ||
				childType >= NtOnelazy && childType <= NtSetlazy && t == NtLazyloop) {
				break
			}
		}

		// child can be too lumpy to blur, e.g., (a {100,105}) {3} or (a {2,})?
		// [but things like (a {2,})+ are not too lumpy...]
		if u.M == 0 && child.M > 1 || child.N < child.M*2 {
			break
		}

		u = child
		if u.M > 0 {
			if (math.MaxInt32-1)/u.M < min {
				u.M = math.MaxInt32
			} else {
				u.M = u.M * min
			}
		}
		if u.N > 0 {
			if (math.MaxInt32-1)/u.N < max {
				u.N = math.MaxInt32
			} else {
				u.N = u.N * max
			}
		}
	}

	if math.MaxInt32 == min {
		return newRegexNode(NtNothing, n.Options)
	}
	return u

}

// Simple optimization. If a concatenation or alternation has only
// one child strip out the intermediate node. If it has zero children,
// turn it into an empty.
func (n *RegexNode) stripEnation(emptyType NodeType) *RegexNode {
	switch len(n.Children) {
	case 0:
		return newRegexNode(emptyType, n.Options)
	case 1:
		return n.Children[0]
	default:
		return n
	}
}

func (n *RegexNode) reduceGroup() *RegexNode {
	u := n

	for u.T == NtGroup {
		u = u.Children[0]
	}

	return u
}

// Simple optimization. If a set is a singleton, an inverse singleton,
// or empty, it's transformed accordingly.
func (n *RegexNode) reduceSet() *RegexNode {
	// Extract empty-set, one and not-one case as special

	if n.Set == nil {
		n.T = NtNothing
	} else if n.Set.IsSingleton() {
		n.Ch = n.Set.SingletonChar()
		n.Set = nil
		n.T += (NtOne - NtSet)
	} else if n.Set.IsSingletonInverse() {
		n.Ch = n.Set.SingletonChar()
		n.Set = nil
		n.T += (NtNotone - NtSet)
	}

	return n
}

func (n *RegexNode) reverseLeft() *RegexNode {
	if n.Options&RightToLeft != 0 && n.T == NtConcatenate && len(n.Children) > 0 {
		//reverse children order
		for left, right := 0, len(n.Children)-1; left < right; left, right = left+1, right-1 {
			n.Children[left], n.Children[right] = n.Children[right], n.Children[left]
		}
	}

	return n
}

func (n *RegexNode) makeQuantifier(lazy bool, min, max int) *RegexNode {
	if min == 0 && max == 0 {
		return newRegexNode(NtEmpty, n.Options)
	}

	if min == 1 && max == 1 {
		return n
	}

	switch n.T {
	case NtOne, NtNotone, NtSet:
		if lazy {
			n.makeRep(Onelazy, min, max)
		} else {
			n.makeRep(Oneloop, min, max)
		}
		return n

	default:
		var t NodeType
		if lazy {
			t = NtLazyloop
		} else {
			t = NtLoop
		}
		result := newRegexNodeMN(t, n.Options, min, max)
		result.addChild(n)
		return result
	}
}

// debug functions

var typeStr = []string{
	"Onerep", "Notonerep", "Setrep",
	"Oneloop", "Notoneloop", "Setloop",
	"Onelazy", "Notonelazy", "Setlazy",
	"One", "Notone", "Set",
	"Multi", "Ref",
	"Bol", "Eol", "Boundary", "Nonboundary",
	"Beginning", "Start", "EndZ", "End",
	"Nothing", "Empty",
	"Alternate", "Concatenate",
	"Loop", "Lazyloop",
	"Capture", "Group", "Require", "Prevent", "Greedy",
	"Testref", "Testgroup",
	"Unknown", "Unknown", "Unknown",
	"Unknown", "Unknown", "Unknown",
	"ECMABoundary", "NonECMABoundary",
}

func (n *RegexNode) description() string {
	buf := &bytes.Buffer{}

	buf.WriteString(typeStr[n.T])

	if (n.Options & ExplicitCapture) != 0 {
		buf.WriteString("-C")
	}
	if (n.Options & IgnoreCase) != 0 {
		buf.WriteString("-I")
	}
	if (n.Options & RightToLeft) != 0 {
		buf.WriteString("-L")
	}
	if (n.Options & Multiline) != 0 {
		buf.WriteString("-M")
	}
	if (n.Options & Singleline) != 0 {
		buf.WriteString("-S")
	}
	if (n.Options & IgnorePatternWhitespace) != 0 {
		buf.WriteString("-X")
	}
	if (n.Options & ECMAScript) != 0 {
		buf.WriteString("-E")
	}

	switch n.T {
	case NtOneloop, NtNotoneloop, NtOnelazy, NtNotonelazy, NtOne, NtNotone:
		buf.WriteString("(Ch = " + CharDescription(n.Ch) + ")")
		break
	case NtCapture:
		buf.WriteString("(index = " + strconv.Itoa(n.M) + ", unindex = " + strconv.Itoa(n.N) + ")")
		break
	case NtRef, NtTestref:
		buf.WriteString("(index = " + strconv.Itoa(n.M) + ")")
		break
	case NtMulti:
		fmt.Fprintf(buf, "(String = %s)", string(n.Str))
		break
	case NtSet, NtSetloop, NtSetlazy:
		buf.WriteString("(Set = " + n.Set.String() + ")")
		break
	}

	switch n.T {
	case NtOneloop, NtNotoneloop, NtOnelazy, NtNotonelazy, NtSetloop, NtSetlazy, NtLoop, NtLazyloop:
		buf.WriteString("(Min = ")
		buf.WriteString(strconv.Itoa(n.M))
		buf.WriteString(", Max = ")
		if n.N == math.MaxInt32 {
			buf.WriteString("inf")
		} else {
			buf.WriteString(strconv.Itoa(n.N))
		}
		buf.WriteString(")")

		break
	}

	return buf.String()
}

var padSpace = []byte("                                ")

func (t *RegexTree) Dump() string {
	return t.Root.dump()
}

func (n *RegexNode) dump() string {
	var stack []int
	CurNode := n
	CurChild := 0

	buf := bytes.NewBufferString(CurNode.description())
	buf.WriteRune('\n')

	for {
		if CurNode.Children != nil && CurChild < len(CurNode.Children) {
			stack = append(stack, CurChild+1)
			CurNode = CurNode.Children[CurChild]
			CurChild = 0

			Depth := len(stack)
			if Depth > 32 {
				Depth = 32
			}
			buf.Write(padSpace[:Depth])
			buf.WriteString(CurNode.description())
			buf.WriteRune('\n')
		} else {
			if len(stack) == 0 {
				break
			}

			CurChild = stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			CurNode = CurNode.Next
		}
	}
	return buf.String()
}
