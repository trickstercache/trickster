package mock

const (
	Kind = string("mock")
)

type Query struct {
	UseTemplate string `yaml:"template"`
	GiveResult  string `yaml:"give_result"`
}

func (q *Query) Template() string {
	return q.UseTemplate
}
