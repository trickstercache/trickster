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

package merge

import (
	"errors"
	"fmt"
	"net/http"
)

// TSMReductionKind identifies how accumulated query variants are combined.
// It describes data flow rather than a provider-specific query operator.
type TSMReductionKind int

const (
	// TSMReductionStandard returns the sole variant accumulator unchanged.
	TSMReductionStandard TSMReductionKind = iota
	// TSMReductionWeightedAverage divides an accumulated sum variant by its
	// paired count variant.
	TSMReductionWeightedAverage
)

const (
	// TSMVariantPrimary is the primary variant.
	TSMVariantPrimary = "primary"
	// TSMVariantWeightedAverageSum is the summed-value input to weighted average.
	TSMVariantWeightedAverageSum = "avg-sum"
	// TSMVariantWeightedAverageCount is the summed-count input to weighted average.
	TSMVariantWeightedAverageCount = "avg-count"
)

// TSMReductionPrimaryVariant returns default primary variant as a single-element slice
func TSMReductionPrimaryVariant() []string {
	return []string{TSMVariantPrimary}
}

// TSMReductionWeightedAverageVariants returns weighted-average input names in
// the positional order required by the reducer: sum, then count.
func TSMReductionWeightedAverageVariants() []string {
	return []string{TSMVariantWeightedAverageSum, TSMVariantWeightedAverageCount}
}

// TSMCompletenessPolicy determines which per-member contributions may enter
// global accumulation.
type TSMCompletenessPolicy int

const (
	// TSMCompletenessResponseAuthority requires the response-authority variant.
	// It is the policy used by standard one-variant plans.
	TSMCompletenessResponseAuthority TSMCompletenessPolicy = iota
	// TSMCompletenessAllVariants requires every variant from the same member.
	TSMCompletenessAllVariants
)

// TSMReductionSpec describes the reduction performed after variant
// accumulation. InputVariants is ordered according to the reduction kind's
// positional contract; use its reduction-specific helper when available.
type TSMReductionSpec struct {
	Kind          TSMReductionKind
	InputVariants []string
}

// TSMFinalizerSpec identifies optional provider finalization after reduction.
type TSMFinalizerSpec struct {
	Enabled bool
	Query   string
}

// TSMQueryVariant is one provider-prepared request in a merge plan.
type TSMQueryVariant struct {
	Name              string
	Request           *http.Request
	MergeStrategy     int
	ResponseAuthority bool
}

// TSMMergePlan is the complete execution plan for one TSM request.
type TSMMergePlan struct {
	OriginalQuery           string
	Variants                []TSMQueryVariant
	Reduction               TSMReductionSpec
	Finalizer               TSMFinalizerSpec
	Completeness            TSMCompletenessPolicy
	UnsupportedWarning      string
	AllowSingleMemberBypass bool
}

// ResponseAuthority returns the index of the variant that supplies response
// status, headers, and marshaling behavior.
func (p *TSMMergePlan) ResponseAuthority() (int, bool) {
	if p == nil {
		return 0, false
	}
	for i := range p.Variants {
		if p.Variants[i].ResponseAuthority {
			return i, true
		}
	}
	return 0, false
}

// Validate rejects plans whose execution semantics are ambiguous or unsafe.
func (p *TSMMergePlan) Validate() error {
	if p == nil {
		return errors.New("tsm merge plan is nil")
	}
	if len(p.Variants) == 0 {
		return errors.New("tsm merge plan has no query variants")
	}

	variantsByName := make(map[string]TSMQueryVariant, len(p.Variants))
	requestPointers := make(map[*http.Request]struct{}, len(p.Variants))
	authorities := 0
	for i, variant := range p.Variants {
		if variant.Name == "" {
			return fmt.Errorf("tsm merge plan variant %d has no name", i)
		}
		if _, ok := variantsByName[variant.Name]; ok {
			return fmt.Errorf("tsm merge plan has duplicate variant name %q", variant.Name)
		}
		variantsByName[variant.Name] = variant
		if variant.Request == nil {
			return fmt.Errorf("tsm merge plan variant %q has no request", variant.Name)
		}
		if _, ok := requestPointers[variant.Request]; ok {
			return fmt.Errorf("tsm merge plan variants share request %q", variant.Name)
		}
		requestPointers[variant.Request] = struct{}{}
		if variant.MergeStrategy < 0 ||
			variant.MergeStrategy > int(MaxStrategyValue) {
			return fmt.Errorf("tsm merge plan variant %q has invalid merge strategy %d",
				variant.Name, variant.MergeStrategy)
		}
		if variant.ResponseAuthority {
			authorities++
		}
	}
	if authorities != 1 {
		return fmt.Errorf("tsm merge plan must have exactly one response authority; got %d", authorities)
	}

	switch p.Reduction.Kind {
	case TSMReductionStandard:
		if len(p.Variants) != 1 {
			return errors.New("standard tsm reduction requires exactly one variant")
		}
		if len(p.Reduction.InputVariants) != 1 ||
			p.Reduction.InputVariants[0] != p.Variants[0].Name {
			return errors.New("standard tsm reduction must name its sole input variant")
		}
		if p.Completeness != TSMCompletenessResponseAuthority {
			return errors.New("standard tsm reduction requires response-authority completeness")
		}
	case TSMReductionWeightedAverage:
		if len(p.Variants) != 2 {
			return errors.New("weighted-average tsm reduction requires exactly two variants")
		}
		if p.OriginalQuery == "" {
			return errors.New("weighted-average tsm reduction requires the original query")
		}
		expectedInputs := TSMReductionWeightedAverageVariants()
		if len(p.Reduction.InputVariants) != len(expectedInputs) {
			return errors.New("weighted-average tsm reduction requires two named inputs")
		}
		for i, expected := range expectedInputs {
			if p.Reduction.InputVariants[i] != expected {
				return fmt.Errorf("weighted-average tsm reduction input %d must be %q", i, expected)
			}
		}
		for _, name := range expectedInputs {
			if variantsByName[name].MergeStrategy != int(StrategySum) {
				return fmt.Errorf("weighted-average tsm variant %q must use sum merge strategy", name)
			}
		}
		if p.Completeness != TSMCompletenessAllVariants {
			return errors.New("weighted-average tsm reduction requires all-variant completeness")
		}
		authority, _ := p.ResponseAuthority()
		if p.Reduction.InputVariants[0] != p.Variants[authority].Name {
			return errors.New("weighted-average tsm reduction must use response authority as its first input")
		}
	default:
		return fmt.Errorf("tsm merge plan has invalid reduction kind %d", p.Reduction.Kind)
	}
	seenInputs := make(map[string]struct{}, len(p.Reduction.InputVariants))
	for _, name := range p.Reduction.InputVariants {
		if _, ok := variantsByName[name]; !ok {
			return fmt.Errorf("tsm merge plan reduction references unknown variant %q", name)
		}
		if _, ok := seenInputs[name]; ok {
			return fmt.Errorf("tsm merge plan reduction repeats variant %q", name)
		}
		seenInputs[name] = struct{}{}
	}

	if p.Finalizer.Enabled && p.Finalizer.Query == "" {
		return errors.New("tsm merge plan finalizer has no query")
	}
	if !p.Finalizer.Enabled && p.Finalizer.Query != "" {
		return errors.New("tsm merge plan has a finalizer query but finalization is disabled")
	}
	if p.AllowSingleMemberBypass &&
		(len(p.Variants) != 1 || p.Reduction.Kind != TSMReductionStandard ||
			p.Finalizer.Enabled || p.UnsupportedWarning != "") {
		return errors.New("tsm merge plan has an unsafe single-member bypass")
	}
	return nil
}
