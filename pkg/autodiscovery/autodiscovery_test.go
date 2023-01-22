package autodiscovery

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
/*
func TestAutodiscovery(t *testing.T) {
	// Create a template backend to test with
	testBackend := &beopt.Options{
		IsTemplate: true,
		OriginURL:  "Fake URL",
	}
	betemp.CreateTemplateBackend("test", testBackend)
	// Create options to test with mock
	testAutodiscovery := &adopt.Options{
		Queries: map[string]*queries.Options{
			"test_query": {
				Method:      "mock",
				UseTemplate: "test_template",
				Parameters: queries.QueryParameters{
					"RequiredParameter":  "MustBeThisValue",
					"SupportedParameter": "AnyValue",
				},
				Results: queries.Results{
					"RequiredResultKey": "TEMPLATE",
				},
			},
		},
		Templates: map[string]*templates.Options{
			"test_template": {
				UseBackend: "test",
				Override: templates.OverrideMap{
					"origin_url": "$[TEMPLATE]",
				},
			},
		},
	}

	// Test with valid mock options
	res, err := DiscoverWithOptions(testAutodiscovery)
	if err != nil {
		t.Fatalf("Failed autodiscovery with valid options; got %+v", err)
	}
	// Result should be exactly 1 backend with name "SomeResult" (always returned by mock to RequiredResultKey)
	if len(res) != 1 {
		t.Fatalf("Autodiscovery with mock method should return 1 result; got %d", len(res))
	}
	if res[0].OriginURL != "SomeResult" {
		t.Fatalf("Autodiscovery with mock method should return SomeResult; got %s", res[0].OriginURL)
	}

	// Okay, let's mess some things up.
	// Remove required parameter
	testFail1 := testAutodiscovery.Clone()
	delete(testFail1.Queries["test_query"].Parameters, "RequiredParameter")
	_, err = DiscoverWithOptions(testFail1)
	if err == nil {
		t.Fatalf("Autodiscovery with missing required parameter should return an error")
	}

	// Remove required result
	testFail2 := testAutodiscovery.Clone()
	delete(testFail2.Queries["test_query"].Results, "RequiredResultKey")
	_, err = DiscoverWithOptions(testFail2)
	if err == nil {
		t.Fatalf("Autodisovery with missing required result key should return an error")
	}
}
*/
