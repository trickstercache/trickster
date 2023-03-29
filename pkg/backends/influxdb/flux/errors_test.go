package flux

import "testing"

func TestInvalidTimeFormatError(t *testing.T) {
	_, err := tryParseTimeField("invalid")
	expected := `invalid time format; must be relative duration (duration literal invalid: expected valid integer value at position 0), RFC3999 string (parsing time "invalid" as "2006-01-02T15:04:05Z07:00": cannot parse "invalid" as "2006"), or Unix timestamp (strconv.Atoi: parsing "invalid": invalid syntax)`
	if err == nil {
		t.Error("expected error")
	} else if err.Error() != expected {
		t.Errorf("got incorrect error %s", err)
	}
}

func TestInvalidFluxError(t *testing.T) {
	err := ErrFluxSyntax("test token", "test rule")
	expected := `flux syntax error at 'test token': test rule`
	if err.Error() != expected {
		t.Errorf("got incorrect error %s", err)
	}
	err = ErrFluxSemantics("test rule")
	expected = `flux semantics error: test rule`
	if err.Error() != expected {
		t.Errorf("got incorrect error %s", err)
	}
}
