package templates

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/options"
	ustrings "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

var templateBackends map[string]*options.Options = make(map[string]*options.Options)

// Stores a set of backend options under a string name.
// Returns an error if the template backend already exists.
func CreateTemplateBackend(name string, opts *options.Options) error {
	if _, exists := templateBackends[name]; exists {
		return fmt.Errorf("Template backend %s already exists.", name)
	} else {
		templateBackends[name] = opts
		return nil
	}
}

// Attempt to get a set of backend options based on their template name.
// Returns backend options, or an error if the given name has no backend through
// CreateTemplateBackend.
func GetTemplateBackend(name string) (*options.Options, error) {
	if betemp, exists := templateBackends[name]; !exists {
		return nil, fmt.Errorf("Template backend %s does not exist.", name)
	} else {
		return betemp, nil
	}
}

// Resolve a template backend.
// This functions by cloning the provided template, then replacing any field with a tag equal to an override key
// with its value.
// Override values will additionally attempt to replace template tokens $[token] with a value provided in
// withValues, i.e override origin_url -> http://$[ORIGIN_URL] and withValues ORIGIN_URL -> localhost
// will replace OriginURL in template with http://localhost.
func ResolveTemplateBackend(template *options.Options, override map[string]string, withValues map[string]string) (*options.Options, error) {
	out := template.Clone()

	// To save iterations, going to create a map of yaml key -> func (set field)
	// Currently only works for string fields.
	yamlSetters := make(map[string]func(string))
	ttype := reflect.TypeOf(*out)
	for i := 0; i < ttype.NumField(); i++ {
		tfield := ttype.Field(i)
		if tfield.Type.Kind() != reflect.String {
			continue
		}
		tag := strings.Split(tfield.Tag.Get("yaml"), ",")[0]
		yamlSetters[tag] = reflect.ValueOf(out).Elem().FieldByName(tfield.Name).SetString
	}

	// Iterate through override and replace
	for replaceTag, replaceVal := range override {
		replaceVal, err := ustrings.TemplateReplace(replaceVal, withValues)
		if err != nil {
			return nil, err
		}
		setter, ok := yamlSetters[replaceTag]
		if ok {
			setter(replaceVal)
		}
	}

	return out, nil
}
