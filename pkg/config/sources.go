/*
 * Copyright 2018 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	yamlencoding "github.com/trickstercache/trickster/v2/pkg/encoding/yaml"

	"go.yaml.in/yaml/v3"
)

const defaultConfigIncludeDirectory = "conf.d"

type configSourceMode uint8

const (
	configSourceModeFile configSourceMode = iota + 1
	configSourceModeDirectory
)

type configSourcePlan struct {
	rootPath             string
	includeDirectoryPath string
	includeRequired      bool
	mode                 configSourceMode
}

type configSource struct {
	path    string
	data    []byte
	modTime time.Time
}

type configSourceSnapshot struct {
	fingerprint  string
	lastModified time.Time
}

type configMainMetadata struct {
	ConfigIncludeDirectory *string `yaml:"config_include_directory"`
}

type configMetadata struct {
	Main *configMainMetadata `yaml:"main"`
}

func loadConfigSources(configPath string) (configSourcePlan, []configSource, error) {
	info, err := os.Stat(configPath)
	if err != nil {
		// Preserve the error shape returned by the previous single-file loader.
		pathError := &os.PathError{}
		if errors.As(err, &pathError) {
			err = &os.PathError{Op: "open", Path: configPath, Err: pathError.Err}
		}
		return configSourcePlan{}, nil, err
	}

	if info.IsDir() {
		plan := configSourcePlan{rootPath: configPath, mode: configSourceModeDirectory}
		sources, err := readConfigSourcePlan(plan)
		if err != nil {
			return configSourcePlan{}, nil, err
		}
		if len(sources) == 0 {
			return configSourcePlan{}, nil, fmt.Errorf("config directory %q contains no supported config files", configPath)
		}
		return plan, sources, nil
	}
	if !info.Mode().IsRegular() {
		return configSourcePlan{}, nil, fmt.Errorf("config path %q is not a regular file or directory", configPath)
	}

	primary, err := readConfigSource(configPath)
	if err != nil {
		return configSourcePlan{}, nil, err
	}
	includeSetting, err := configIncludeDirectoryFromYAML(primary.data)
	if err != nil {
		return configSourcePlan{}, nil, fmt.Errorf("parse config file %q: %w", configPath, err)
	}
	plan := configSourcePlan{
		rootPath:        configPath,
		includeRequired: includeSetting != nil && *includeSetting != "",
		mode:            configSourceModeFile,
	}
	plan.includeDirectoryPath = resolveConfigIncludeDirectory(configPath, includeSetting)

	paths, err := plan.paths()
	if err != nil {
		return configSourcePlan{}, nil, err
	}
	sources := make([]configSource, 0, len(paths))
	sources = append(sources, primary)
	for _, path := range paths[1:] {
		source, err := readConfigSource(path)
		if err != nil {
			return configSourcePlan{}, nil, err
		}
		sources = append(sources, source)
	}
	return plan, sources, nil
}

func configIncludeDirectoryFromYAML(data []byte) (*string, error) {
	document := &yaml.Node{}
	if err := yaml.Unmarshal(data, document); err != nil {
		return nil, err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, nil
	}
	metadata := configMetadata{}
	if err := document.Decode(&metadata); err != nil {
		return nil, err
	}
	if metadata.Main == nil {
		return nil, nil
	}
	return metadata.Main.ConfigIncludeDirectory, nil
}

func resolveConfigIncludeDirectory(configPath string, configuredDirectory *string) string {
	if configuredDirectory == nil || *configuredDirectory == "" {
		return filepath.Join(filepath.Dir(configPath), defaultConfigIncludeDirectory)
	}
	if filepath.IsAbs(*configuredDirectory) {
		return filepath.Clean(*configuredDirectory)
	}
	return filepath.Join(filepath.Dir(configPath), *configuredDirectory)
}

func (plan configSourcePlan) paths() ([]string, error) {
	if plan.mode == configSourceModeDirectory {
		paths, err := configPathsFromDirectory(plan.rootPath, "")
		if err != nil {
			return nil, fmt.Errorf("read config directory %q: %w", plan.rootPath, err)
		}
		return paths, nil
	}

	paths := []string{plan.rootPath}
	fragments, err := configPathsFromDirectory(plan.includeDirectoryPath, plan.rootPath)
	if err != nil {
		if !plan.includeRequired && errors.Is(err, os.ErrNotExist) {
			return paths, nil
		}
		return nil, fmt.Errorf("read config include directory %q: %w", plan.includeDirectoryPath, err)
	}
	return append(paths, fragments...), nil
}

func configPathsFromDirectory(directoryPath, excludedPath string) ([]string, error) {
	entries, err := os.ReadDir(directoryPath)
	if err != nil {
		return nil, err
	}

	var excludedInfo os.FileInfo
	if excludedPath != "" {
		excludedInfo, _ = os.Stat(excludedPath)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || !isConfigSourceName(entry.Name()) {
			continue
		}
		path := filepath.Join(directoryPath, entry.Name())
		info, err := os.Stat(path) // #nosec G703 -- the path is selected from an operator-provided config directory
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || excludedInfo != nil && os.SameFile(excludedInfo, info) {
			continue
		}
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths, nil
}

func isConfigSourceName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".conf", ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func readConfigSourcePlan(plan configSourcePlan) ([]configSource, error) {
	paths, err := plan.paths()
	if err != nil {
		return nil, err
	}
	sources := make([]configSource, 0, len(paths))
	for _, path := range paths {
		source, err := readConfigSource(path)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func readConfigSource(path string) (configSource, error) {
	data, err := os.ReadFile(path) // #nosec G703 -- the path is selected from an operator-provided config path
	if err != nil {
		return configSource{}, fmt.Errorf("read config source %q: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return configSource{}, fmt.Errorf("stat config source %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return configSource{}, fmt.Errorf("config source %q is not a regular file", path)
	}
	return configSource{path: path, data: data, modTime: info.ModTime()}, nil
}

func mergeConfigSources(plan configSourcePlan, sources []configSource) ([]byte, error) {
	merged := emptyConfigDocument()
	for index, source := range sources {
		document, err := parseConfigDocument(source.data)
		if err != nil {
			return nil, fmt.Errorf("parse config source %q: %w", source.path, err)
		}
		includeSetting, err := configIncludeDirectoryFromYAML(source.data)
		if err != nil {
			return nil, fmt.Errorf("parse config source %q: %w", source.path, err)
		}
		if includeSetting != nil && (plan.mode == configSourceModeDirectory || index > 0) {
			return nil, fmt.Errorf("config source %q cannot set main.config_include_directory", source.path)
		}
		mergeConfigMapping(merged.Content[0], document.Content[0])
	}
	data, err := yamlencoding.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("marshal merged configuration: %w", err)
	}
	return data, nil
}

func emptyConfigDocument() *yaml.Node {
	return &yaml.Node{
		Kind: yaml.DocumentNode,
		Content: []*yaml.Node{{
			Kind: yaml.MappingNode,
			Tag:  "!!map",
		}},
	}
}

func parseConfigDocument(data []byte) (*yaml.Node, error) {
	document := &yaml.Node{}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(document); err != nil {
		if errors.Is(err, io.EOF) {
			return emptyConfigDocument(), nil
		}
		return nil, err
	}
	extra := &yaml.Node{}
	if err := decoder.Decode(extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("multiple YAML documents are not supported")
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("configuration document root must be a mapping")
	}
	if err := validateConfigMappingKeys(document.Content[0]); err != nil {
		return nil, err
	}
	expanded, err := cloneConfigNodeWithoutAliases(document, make(map[*yaml.Node]bool))
	if err != nil {
		return nil, err
	}
	return expanded, nil
}

func cloneConfigNodeWithoutAliases(node *yaml.Node, active map[*yaml.Node]bool) (*yaml.Node, error) {
	if node == nil {
		return nil, errors.New("YAML alias does not reference a node")
	}
	if active[node] {
		return nil, fmt.Errorf("YAML alias cycle at line %d is not supported", node.Line)
	}
	active[node] = true
	defer delete(active, node)

	if node.Kind == yaml.AliasNode {
		return cloneConfigNodeWithoutAliases(node.Alias, active)
	}
	clone := *node
	clone.Anchor = ""
	clone.Alias = nil
	clone.Content = make([]*yaml.Node, 0, len(node.Content))
	for _, child := range node.Content {
		childClone, err := cloneConfigNodeWithoutAliases(child, active)
		if err != nil {
			return nil, err
		}
		clone.Content = append(clone.Content, childClone)
	}
	return &clone, nil
}

func validateConfigMappingKeys(node *yaml.Node) error {
	switch node.Kind {
	case yaml.MappingNode:
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode {
				return fmt.Errorf("configuration mapping key at line %d must be a scalar", key.Line)
			}
			if _, ok := seen[key.Value]; ok {
				return fmt.Errorf("configuration mapping key %q is repeated at line %d", key.Value, key.Line)
			}
			seen[key.Value] = struct{}{}
			if err := validateConfigMappingKeys(node.Content[index+1]); err != nil {
				return err
			}
		}
	case yaml.SequenceNode, yaml.DocumentNode:
		for _, child := range node.Content {
			if err := validateConfigMappingKeys(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func mergeConfigMapping(destination, overlay *yaml.Node) {
	for index := 0; index < len(overlay.Content); index += 2 {
		key := overlay.Content[index]
		value := overlay.Content[index+1]
		destinationValueIndex := configMappingValueIndex(destination, key.Value)
		if destinationValueIndex < 0 {
			destination.Content = append(destination.Content, key, value)
			continue
		}
		destinationValue := destination.Content[destinationValueIndex]
		if destinationValue.Kind == yaml.MappingNode && value.Kind == yaml.MappingNode {
			mergeConfigMapping(destinationValue, value)
			continue
		}
		destination.Content[destinationValueIndex] = value
	}
}

func configMappingValueIndex(mapping *yaml.Node, key string) int {
	for index := len(mapping.Content) - 2; index >= 0; index -= 2 {
		if mapping.Content[index].Value == key {
			return index + 1
		}
	}
	return -1
}

func snapshotConfigSources(plan configSourcePlan, sources []configSource, sourceErr error) configSourceSnapshot {
	hash := sha256.New()
	fmt.Fprintf(hash, "mode:%d\x00root:%s\x00include:%s\x00required:%t\x00",
		plan.mode, filepath.Clean(plan.rootPath), filepath.Clean(plan.includeDirectoryPath), plan.includeRequired)

	snapshot := configSourceSnapshot{}
	for _, source := range sources {
		fmt.Fprintf(hash, "source:%s\x00mtime:%d\x00size:%d\x00",
			filepath.Clean(source.path), source.modTime.UnixNano(), len(source.data))
		hash.Write(source.data)
		hash.Write([]byte{0})
		if source.modTime.After(snapshot.lastModified) {
			snapshot.lastModified = source.modTime
		}
	}
	if sourceErr != nil {
		fmt.Fprintf(hash, "error:%T:%s\x00", sourceErr, sourceErr)
	}
	snapshot.fingerprint = hex.EncodeToString(hash.Sum(nil))
	return snapshot
}

func inspectConfigSources(plan configSourcePlan) configSourceSnapshot {
	sources, err := readConfigSourcePlan(plan)
	return snapshotConfigSources(plan, sources, err)
}
