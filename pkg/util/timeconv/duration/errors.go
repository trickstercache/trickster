package duration

import "fmt"

type ErrInvalidDurationFormat struct {
	position int
	expected string
	in       string
}

func InvalidDurationFormat(pos int, expected string, in string) *ErrInvalidDurationFormat {
	return &ErrInvalidDurationFormat{
		position: pos,
		expected: expected,
		in:       in,
	}
}

func (err *ErrInvalidDurationFormat) Error() string {
	return fmt.Sprintf("duration literal %s: expected %s at position %d", err.in, err.expected, err.position)
}

type ErrUnableToParse struct {
	literal string
}

func UnableToParse(literal string) *ErrUnableToParse {
	return &ErrUnableToParse{
		literal: literal,
	}
}

func (err *ErrUnableToParse) Error() string {
	return fmt.Sprintf("duration literal %s: reached end of literal without finding a valid duration format", err.literal)
}
