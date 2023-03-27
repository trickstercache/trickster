package flux

import "fmt"

type InvalidTimeFormatError struct {
	rd error
	at error
	ut error
}

type FluxSyntaxError struct {
	token string
	rule  string
}

type FluxSemanticsError struct {
	rule string
}

func (err *InvalidTimeFormatError) Error() string {
	return fmt.Sprintf("invalid time format; must be relative duration (%s), RFC3999 string (%s), or Unix timestamp (%s)", err.rd, err.at, err.ut)
}

func ErrInvalidTimeFormat(relativeDuration, absoluteTime, unixTimestamp error) *InvalidTimeFormatError {
	return &InvalidTimeFormatError{
		rd: relativeDuration,
		at: absoluteTime,
		ut: unixTimestamp,
	}
}

func ErrFluxSyntax(token, rule string) error {
	return &FluxSyntaxError{
		token: token,
		rule:  rule,
	}
}

func (err *FluxSyntaxError) Error() string {
	return fmt.Sprintf("flux syntax error at '%s': %s", err.token, err.rule)
}

func ErrFluxSemantics(rule string) error {
	return &FluxSemanticsError{
		rule: rule,
	}
}

func (err *FluxSemanticsError) Error() string {
	return fmt.Sprintf("flux semantics error: %s", err.rule)
}
