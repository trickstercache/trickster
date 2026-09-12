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

package options

// Request parts an instruction addresses, spelled as its first element
const (
	PartScheme   = "scheme"
	PartHeader   = "header"
	PartPath     = "path"
	PartParam    = "param"
	PartParams   = "params"
	PartMethod   = "method"
	PartHost     = "host"
	PartHostname = "hostname"
	PartPort     = "port"
	PartChain    = "chain"
)

// Actions an instruction performs on its part, spelled as its second element
const (
	ActionSet           = "set"
	ActionReplace       = "replace"
	ActionDelete        = "delete"
	ActionAppend        = "append"
	ActionPrefixReplace = "prefix-replace"
	ActionExec          = "exec"
)

// PathSet returns the instruction that replaces the request path
func PathSet(path string) []string {
	return []string{PartPath, ActionSet, path}
}

// PathPrefixReplace returns the instruction that replaces a leading path
// prefix, matched on a segment boundary, and keeps the rest of the path
func PathPrefixReplace(prefix, replacement string) []string {
	return []string{PartPath, ActionPrefixReplace, prefix, replacement}
}

// SchemeSet returns the instruction that replaces the request scheme
func SchemeSet(scheme string) []string {
	return []string{PartScheme, ActionSet, scheme}
}

// HostnameSet returns the instruction that replaces the hostname, keeping the port
func HostnameSet(hostname string) []string {
	return []string{PartHostname, ActionSet, hostname}
}

// PortSet returns the instruction that replaces the port, keeping the hostname
func PortSet(port string) []string {
	return []string{PartPort, ActionSet, port}
}

// HeaderSet returns the instruction that sets a header to one value
func HeaderSet(name, value string) []string {
	return []string{PartHeader, ActionSet, name, value}
}

// HeaderAppend returns the instruction that appends a value to a header
func HeaderAppend(name, value string) []string {
	return []string{PartHeader, ActionAppend, name, value}
}

// HeaderDelete returns the instruction that removes a header
func HeaderDelete(name string) []string {
	return []string{PartHeader, ActionDelete, name}
}
