// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2018 Datadog, Inc.

package ddsketch

import (
	"math"
	"reflect"
)

const (
	defaultMaxNumBins = 2048
	defaultAlpha      = 0.01
	defaultMinValue   = 1.0e-9
)

// Config contains an offset for the bin keys which ensures that keys for positive
// numbers that are larger than minValue are greater than or equal to 1 while the
// keys for negative numbers are less than or equal to -1.
type Config struct {
	maxNumBins int
	gamma      float64
	gammaLn    float64
	minValue   float64
	offset     int
}

func NewDefaultConfig() *Config {
	return NewConfig(defaultAlpha, defaultMaxNumBins, defaultMinValue)
}

func NewConfig(alpha float64, maxNumBins int, minValue float64) *Config {
	c := &Config{
		maxNumBins: maxNumBins,
		gamma:      1 + 2*alpha/(1-alpha),
		gammaLn:    math.Log1p(2 * alpha / (1 - alpha)),
		minValue:   minValue,
	}
	c.offset = -int(c.logGamma(c.minValue)) + 1
	return c
}

func (c *Config) Key(v float64) int {
	if v < -c.minValue {
		return -int(math.Ceil(c.logGamma(-v))) - c.offset
	} else if v > c.minValue {
		return int(math.Ceil(c.logGamma(v))) + c.offset
	} else {
		return 0
	}
}

func (c *Config) logGamma(v float64) float64 {
	return math.Log(v) / c.gammaLn
}

func (c *Config) powGamma(k int) float64 {
	return math.Exp(float64(k) * c.gammaLn)
}

func (c *Config) Size() int {
	return int(reflect.TypeOf(*c).Size())
}
