package timeconv

import "fmt"

type InvalidDurationFormatError struct {
	position int
	expected string
	in       string
}

func InvalidDurationFormatErr(pos int, expected string, in string) *InvalidDurationFormatError {
	return &InvalidDurationFormatError{
		position: pos,
		expected: expected,
		in:       in,
	}
}

func (err *InvalidDurationFormatError) Error() string {
	return fmt.Sprintf("duration literal %s: expected %s at position %d", err.in, err.expected, err.position)
}

type UnableToParseError struct {
	literal string
}

func UnableToParseErr(literal string) *UnableToParseError {
	return &UnableToParseError{
		literal: literal,
	}
}

func (err *UnableToParseError) Error() string {
	return fmt.Sprintf("duration literal %s: reached end of literal without finding a valid duration format", err.literal)
}
