# Adding a New Configuration Value

Trickster configurations are defined in `options` packages below each feature package (e.g., `/backends/options`, `caches/options`, etc) and are mapped to `yaml` struct tags.

When adding a configuration value, there are several places to add references, which are described below.

## Configuration Code

Each new configuration value must be defined in an `options` package under an existing Configuration collection (Origins, Caches, Paths, etc.).

Make sure the YAML annotation uses a `lowercase_no_spaces` naming convention, while the configuration member name itself should be `CamelCase`. Follow the existing configs for guidance.

## Feature Code

Once you have defined your configuration value(s), you must put them to work by referencing them elsewhere in the Trickster code, and used to determine or customize the application functionality. Exactly where this happens in the code depends upon the context and reach of your new configuration, and what features its state affects. Consult with a project maintainer if you have any questions.

## Tests

All new values that you add should have accompanying unit tests to ensure the modifications the value makes to the application in the feature code work as designed. Unit Tests should include verification of: proper parsing of configuration value from test config files (in ./testdata), correct feature functionality enable/disable based on the configuration value, correct feature implementation, coverage of all executable lines of code. 

## Documentation

The feature should be documented under `./docs` directory, in a suitable existing or new markdown file based on the nature of the feature. The documentation should show the key example configuration options and describe their expected results, and point to the example config file for more information.

The [example config file](../examples/conf/example.full.yaml) should be updated to include the exhaustive description and options for the configuration value(s).

## Deployment

The `./deploy/kube/configmap.yaml` must be updated to include the new configuration option(s). Generally this file contains a copy/paste of [example.full.yaml](../examples/conf/example.full.yaml).

The `./deploy/helm/trickster/values.yaml` file must be updated to mirror the configuration option(s) in `example.full.yaml`, and `./deploy/helm/trickster/templates/configmap.yaml` must be updated to map any new `yamlCaseValues` to their respective snake case values for config file generation via the template.
