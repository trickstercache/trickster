package autodiscovery

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients/etcd"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients/kube"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/clients/mock"
	adopt "github.com/trickstercache/trickster/v2/pkg/autodiscovery/options"
	beopt "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/templates"
	"gopkg.in/yaml.v3"
)

func TestValidClients(t *testing.T) {
	names := []string{etcd.Provider, kube.Provider, mock.Provider}
	for _, name := range names {
		if !clients.IsSupportedClient(name) {
			t.Errorf("expected %s to be supported", name)
		}
	}
}

/*
	func TestRegisteredMethods(t *testing.T) {
		// Run through a list of registered methods by name, check that:
		//   - Every name is valid
		//   - Every name returns a struct that implements Method
		//   - The resulting Method has the same name
		names := []string{"mock", "kubernetes_external"}
		for _, name := range names {
			valid := methods.IsSupportedADMethod(name)
			if !valid {
				t.Errorf("Method %s returned false for IsSupportedADMethod", name)
				continue
			}
			method, err := methods.GetMethod(name)
			if err != nil {
				t.Errorf("Method %s returned error for GetMethod; %+v", name, err)
			}
			if method.Name() != name {
				t.Errorf("Method %s returned incorrect method (or implementation of Name() is incorrect); got %s", name, method.Name())
			}
		}
	}
*/

var testconf string = `
clients:
  test_client:
    provider: mock
    queries: [test_query]
queries:
  test_query:
    provider: mock
    give_result: test
    template: test_template
templates:
  test_template:
    use_backend: test
    override:
      origin_url: $[result]
`

func TestAutodiscovery(t *testing.T) {
	// Create a template backend to test with
	testBackend := &beopt.Options{
		IsTemplate: true,
		OriginURL:  "Fake URL",
	}
	templates.CreateTemplateBackend("test", testBackend)
	// Create options to test with mock
	var disco *adopt.Options = adopt.New()
	err := yaml.Unmarshal([]byte(testconf), &disco)
	if err != nil {
		t.Fatal(err)
	}

	// Test with valid mock options
	res, err := DiscoverWithOptions(disco)
	if err != nil {
		t.Fatalf("Failed autodiscovery with valid options; got %+v", err)
	}
	// Result should be exactly 1 backend with name "SomeResult" (always returned by mock to RequiredResultKey)
	if len(res) != 1 {
		t.Fatalf("Autodiscovery with mock method should return 1 result; got %d", len(res))
	}
	if res[0].OriginURL != "test" {
		t.Fatalf("Autodiscovery with mock method should return SomeResult; got %s", res[0].OriginURL)
	}
}
