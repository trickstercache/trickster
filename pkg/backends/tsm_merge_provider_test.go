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

package backends

import (
	"net/http"
	"testing"
)

func testTSMRequest(path string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "http://example.com/"+path, nil)
	return r
}

func validStandardTSMPlan() *TSMMergePlan {
	return &TSMMergePlan{
		OriginalQuery: "sum(up)",
		Variants: []TSMQueryVariant{{
			Name:              "primary",
			Request:           testTSMRequest("primary"),
			MergeStrategy:     1,
			ResponseAuthority: true,
		}},
		Reduction: TSMReductionSpec{
			Kind:          TSMReductionStandard,
			InputVariants: []string{"primary"},
		},
		Completeness:            TSMCompletenessResponseAuthority,
		AllowSingleMemberBypass: true,
	}
}

func validWeightedAverageTSMPlan() *TSMMergePlan {
	return &TSMMergePlan{
		OriginalQuery: "avg(up)",
		Variants: []TSMQueryVariant{
			{
				Name:              "avg-sum",
				Request:           testTSMRequest("sum"),
				MergeStrategy:     1,
				ResponseAuthority: true,
			},
			{
				Name:          "avg-count",
				Request:       testTSMRequest("count"),
				MergeStrategy: 1,
			},
		},
		Reduction: TSMReductionSpec{
			Kind:          TSMReductionWeightedAverage,
			InputVariants: []string{"avg-sum", "avg-count"},
		},
		Completeness: TSMCompletenessAllVariants,
	}
}

func TestTSMMergePlanValidate(t *testing.T) {
	if err := validStandardTSMPlan().Validate(); err != nil {
		t.Fatalf("valid standard plan: %v", err)
	}
	if err := validWeightedAverageTSMPlan().Validate(); err != nil {
		t.Fatalf("valid weighted-average plan: %v", err)
	}
	var nilPlan *TSMMergePlan
	if err := nilPlan.Validate(); err == nil {
		t.Fatal("nil plan unexpectedly validated")
	}

	tests := []struct {
		name   string
		plan   func() *TSMMergePlan
		mutate func(*TSMMergePlan)
	}{
		{"zero variants", validStandardTSMPlan, func(p *TSMMergePlan) { p.Variants = nil }},
		{"empty variant name", validStandardTSMPlan, func(p *TSMMergePlan) { p.Variants[0].Name = "" }},
		{"nil request", validStandardTSMPlan, func(p *TSMMergePlan) { p.Variants[0].Request = nil }},
		{"invalid low strategy", validStandardTSMPlan, func(p *TSMMergePlan) { p.Variants[0].MergeStrategy = -1 }},
		{"invalid high strategy", validStandardTSMPlan, func(p *TSMMergePlan) { p.Variants[0].MergeStrategy = 6 }},
		{"missing authority", validStandardTSMPlan, func(p *TSMMergePlan) { p.Variants[0].ResponseAuthority = false }},
		{"standard missing reduction input", validStandardTSMPlan, func(p *TSMMergePlan) { p.Reduction.InputVariants = nil }},
		{"standard wrong completeness", validStandardTSMPlan, func(p *TSMMergePlan) { p.Completeness = TSMCompletenessAllVariants }},
		{"invalid reduction", validStandardTSMPlan, func(p *TSMMergePlan) { p.Reduction.Kind = 99 }},
		{"enabled finalizer missing query", validStandardTSMPlan, func(p *TSMMergePlan) { p.Finalizer.Enabled = true }},
		{"disabled finalizer with query", validStandardTSMPlan, func(p *TSMMergePlan) { p.Finalizer.Query = "sum(up)" }},
		{"bypass with warning", validStandardTSMPlan, func(p *TSMMergePlan) { p.UnsupportedWarning = "inexact" }},
		{"duplicate variant name", validWeightedAverageTSMPlan, func(p *TSMMergePlan) { p.Variants[1].Name = "avg-sum" }},
		{"shared variant request", validWeightedAverageTSMPlan, func(p *TSMMergePlan) { p.Variants[1].Request = p.Variants[0].Request }},
		{"multiple authorities", validWeightedAverageTSMPlan, func(p *TSMMergePlan) { p.Variants[1].ResponseAuthority = true }},
		{"weighted missing reduction input", validWeightedAverageTSMPlan, func(p *TSMMergePlan) { p.Reduction.InputVariants = []string{"avg-sum"} }},
		{"weighted missing original query", validWeightedAverageTSMPlan, func(p *TSMMergePlan) { p.OriginalQuery = "" }},
		{"weighted duplicate reduction input", validWeightedAverageTSMPlan, func(p *TSMMergePlan) { p.Reduction.InputVariants[1] = "avg-sum" }},
		{"weighted unknown reduction input", validWeightedAverageTSMPlan, func(p *TSMMergePlan) { p.Reduction.InputVariants[1] = "missing" }},
		{"weighted wrong completeness", validWeightedAverageTSMPlan, func(p *TSMMergePlan) { p.Completeness = TSMCompletenessResponseAuthority }},
		{"weighted authority not first input", validWeightedAverageTSMPlan, func(p *TSMMergePlan) {
			p.Variants[0].ResponseAuthority = false
			p.Variants[1].ResponseAuthority = true
		}},
		{"multi variant bypass", validWeightedAverageTSMPlan, func(p *TSMMergePlan) { p.AllowSingleMemberBypass = true }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := test.plan()
			test.mutate(plan)
			if err := plan.Validate(); err == nil {
				t.Fatal("invalid plan unexpectedly validated")
			}
		})
	}
}

func TestTSMMergePlanResponseAuthority(t *testing.T) {
	plan := validWeightedAverageTSMPlan()
	index, ok := plan.ResponseAuthority()
	if !ok || index != 0 {
		t.Fatalf("authority: got (%d, %v), want (0, true)", index, ok)
	}
	plan.Variants[0].ResponseAuthority = false
	if _, ok := plan.ResponseAuthority(); ok {
		t.Fatal("missing authority unexpectedly reported")
	}
}
