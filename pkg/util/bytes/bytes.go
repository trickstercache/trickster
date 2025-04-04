package bytes

const maxInt = int(^uint(0) >> 1)

func MergeSlices(a, b []byte) []byte {
	lenA := len(a)
	lenB := len(b)
	if lenA > maxInt {
		return a[:maxInt]
	}
	remaining := maxInt - lenA
	if lenB > remaining {
		b = b[:remaining]
	}
	out := make([]byte, lenA+len(b))
	copy(out, a)
	copy(out[lenA:], b)
	return out
}
