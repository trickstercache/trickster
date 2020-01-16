# Adding a New Configuration Value

Trickster configurations are defined in `./internal/config/` and are mapped to `toml` annotations.

When adding a configuration value, there are several places to add references, which are described below.

## Configuration Code

Each new configuration value must be defined in the `config` package under an existing Configuration collection (Origins, Caches, Paths, etc.).

Make sure the TOML annotation uses a `lowercase_no_spaces` naming convention, while the configuration member name itself should be `CamelCase`. Follow the existing configs for guidance.

Once you have defined the configuration member, if it is part of a `CacheConfig`, `OriginConfig` or `PathConfig`, it must also be added to the configuration parser for the specific type of configruation. These methods iterate through known TOML annoations to survey which configs have been set. This allows Trickster to know if a value is set because it is the initialized default value or because the operator has explicitly set it.

## Feature Code

Once you have defined your configuration value(s), you must put them to work by referencing them elsewhere in the Trickster code, and used to determine or customize the application functionality. Exactly where this happens in the code depends upon the context and reach or your new configuration, and what features its state affects. Consult with a project maintainer if you have any questions.

## Tests

All new values that you add should have accompanying unit tests to ensure the modifications the value makes to the application in the feature code work as designed. Unit Tests should include verification of: proper parsing of configuration value from test config files (in ./testdata), correct feature functionality enable/disable based on the configuration value, correct feature implementation, coverage of all executable lines of code. Unit Test will span the `config` package, and any package(s) wherein the configuration value is used by the applciation.

## Documentation

The feature should be documented under `./docs` directory, in a suitable existing or new markdown file based on the nature of the feature. The documentation should show the key example configuration options and describe their expected results, and point to the example config file for more information.

The example config file (./cmd/trickster/conf/example.conf) should be updated to include the exhaustive description and options for the configuration value(s).

## Deployment

The `./deply/kube/configmap.yaml` must be updated to include the new configuration option(s). Generally this file contains a copy/paste of `./cmd/trickster/conf/example.conf`.

The `./deploy/helm/trickster/values.yaml` file must be updated to mirror the configuration option(s) in the example.conf, and `./deploy/helm/trickster/templates/configmap.yaml` must be updated to map any new `yamlCaseValues` to their respective `toml_style_values` for config file generation via the template.

