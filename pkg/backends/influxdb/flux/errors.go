package flux

import "fmt"

type ErrFluxSyntax struct {
	token string
	rule  string
}

func FluxSyntax(token, rule string) *ErrFluxSyntax {
	return &ErrFluxSyntax{
		token: token,
		rule:  rule,
	}
}

func (err *ErrFluxSyntax) Error() string {
	return fmt.Sprintf("flux syntax error at '%s': %s", err.token, err.rule)
}

type ErrFluxSemantics struct {
	rule string
}

func FluxSemantics(rule string) *ErrFluxSemantics {
	return &ErrFluxSemantics{
		rule: rule,
	}
}

func (err *ErrFluxSemantics) Error() string {
	return fmt.Sprintf("flux semantics error: %s", err.rule)
}
