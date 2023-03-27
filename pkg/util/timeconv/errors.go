package timeconv

import "fmt"

type InvalidDurationFormatError struct {
	position int
	expected string
	in       string
}

func ErrInvalidDurationFormat(pos int, expected string, in string) *InvalidDurationFormatError {
	return &InvalidDurationFormatError{
		position: pos,
		expected: expected,
		in:       in,
	}
}

func (err *InvalidDurationFormatError) Error() string {
	return fmt.Sprintf("duration literal %s: expected %s at position %d", err.in, err.expected, err.position)
}
