package linestate

// Counts is the line-level change between two versions of a file. Every
// changed line is in exactly one of Tail or Mid, so Tail+Mid == Added+Removed.
type Counts struct {
	Added, Removed int
	Tail           int // changed lines in the last ~10% of the new file
	Mid            int // changed lines anywhere before that
}

// maxEdits bounds the Myers search. Past it the changed region is counted as
// wholly replaced: still a correct upper bound, and it keeps a reformat of a
// huge file from costing seconds and megabytes on every tick.
const maxEdits = 1000

// Diff counts the lines added and removed going from a to b, and where in b
// they fall. Common head and tail are trimmed first, so the usual edit --- a
// few lines in one place --- never reaches the quadratic part at all.
func Diff(a, b []uint64) Counts {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	am, bm := a[pre:len(a)-suf], b[pre:len(b)-suf]

	var c Counts
	tailFrom := len(b) * 9 / 10
	at := func(pos int) {
		if pos >= tailFrom {
			c.Tail++
		} else {
			c.Mid++
		}
	}
	ins := func(bi int) { c.Added++; at(pre + bi) }
	del := func(bi int) { c.Removed++; at(pre + bi) }

	if !myers(am, bm, ins, del) {
		for i := range bm {
			ins(i)
		}
		for i := range am {
			del(min(i, len(bm)))
		}
	}
	return c
}

// myers runs Myers' O(ND) diff and reports each inserted line by its index in
// b, and each deleted line by the index in b it was removed before. It returns
// false, having reported nothing, if the edit distance exceeds maxEdits.
func myers(a, b []uint64, ins, del func(bi int)) bool {
	n, m := len(a), len(b)
	limit := min(n+m, maxEdits)
	off := limit + 1
	v := make([]int, 2*off+1)
	// trace[d] is v as it stood before step d, windowed to k in [-d-1, d+1],
	// which is all the backtrack reads --- O(D²) memory, not O(D·(N+M)).
	var trace [][]int

	for d := 0; d <= limit; d++ {
		trace = append(trace, append([]int(nil), v[off-d-1:off+d+2]...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				backtrack(trace, d, n, m, ins, del)
				return true
			}
		}
	}
	return false
}

func backtrack(trace [][]int, D, x, y int, ins, del func(bi int)) {
	for d := D; d > 0; d-- {
		vp := trace[d]
		get := func(k int) int { return vp[k+d+1] }
		k := x - y
		pk := k - 1
		if k == -d || (k != d && get(k-1) < get(k+1)) {
			pk = k + 1
		}
		px := get(pk)
		py := px - pk
		if pk == k+1 {
			ins(py) // moved down: b[py] was inserted
		} else {
			del(py) // moved right: a[px] was removed, before b[py]
		}
		x, y = px, py
	}
}
