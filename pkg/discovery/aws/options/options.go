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

// Package options defines the API and credential settings for the aws
// autodiscovery provider.
package options

import (
	"errors"
	"slices"
	"strings"

	taws "github.com/trickstercache/trickster/v2/pkg/aws"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// AWS services the 'aws' discovery provider can read from
const (
	// ServiceEC2 discovers EC2 instances via DescribeInstances
	ServiceEC2 = "ec2"
)

// ErrInvalidService is returned when aws.service names an unsupported API
var ErrInvalidService = errors.New(
	"'service' must be one of " + strings.Join([]string{ServiceEC2}, ", "))

// SupportedServices returns the aws.service values this build supports.
func SupportedServices() []string { return []string{ServiceEC2} }

// Options defines the API and credential settings for a discoverer with the
// 'aws' provider.
//
// 'service' selects which AWS API to read, and doubles as the SigV4 signing
// service name -- one field, both meanings, correctly aligned. Note it is
// deliberately not called 'role': the credential fields below already
// include 'role_arn', and 'role: ec2' beside
// 'role_arn: arn:aws:iam::...' would read as though the first were an IAM
// role name. (Prometheus calls the equivalent field 'role'.)
//
// Leaving the credential fields empty selects the standard AWS credential
// chain -- environment, shared config, EKS IRSA and Pod Identity, SSO,
// instance metadata. See docs/aws.md.
type Options struct {
	// Service names the AWS API to discover from; see SupportedServices
	Service string `yaml:"service,omitempty"`
	// Region is the AWS region to query. When empty it is resolved from the
	// environment, the shared config file, or instance metadata.
	Region string `yaml:"region,omitempty"`
	// AccessKey and SecretKey are a static credential pair; both or neither
	AccessKey string      `yaml:"access_key,omitempty"`
	SecretKey taws.Secret `yaml:"secret_key,omitempty"`
	// Profile names a profile in the shared config file
	Profile string `yaml:"profile,omitempty"`
	// RoleARN is a role to assume with whatever the chain resolves first
	RoleARN string `yaml:"role_arn,omitempty"`
}

// New returns an Options with default values
func New() *Options { return &Options{Service: ServiceEC2} }

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options { return pointers.Clone(o) }

// Initialize applies defaults
func (o *Options) Initialize() {
	if o == nil {
		return
	}
	if o.Service == "" {
		o.Service = ServiceEC2
	}
}

// GetService returns the configured AWS API, or the default.
func (o *Options) GetService() string {
	if o == nil || o.Service == "" {
		return ServiceEC2
	}
	return o.Service
}

// SignerOptions renders the credential settings for pkg/aws, deriving the
// signing service from the discovery service.
func (o *Options) SignerOptions() *taws.Options {
	if o == nil {
		return nil
	}
	return &taws.Options{
		Region:    o.Region,
		AccessKey: o.AccessKey,
		SecretKey: o.SecretKey,
		Profile:   o.Profile,
		RoleARN:   o.RoleARN,
		Service:   o.GetService(),
	}
}

// Validate validates the Options
func (o *Options) Validate() error {
	if o == nil {
		return errors.New("the 'aws' block is required")
	}
	if o.Service != "" && !slices.Contains(SupportedServices(), o.Service) {
		return ErrInvalidService
	}
	return o.SignerOptions().Validate()
}
