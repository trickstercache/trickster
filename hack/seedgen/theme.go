/*
 * Copyright 2026 The Trickster Authors
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

package main

import (
	"fmt"
	"sort"
	"strconv"
)

// This file is the whole theme: the fictional city of Emberwick, its
// boroughs, neighborhoods, cab colours and vendor codes. Edit only this file
// to re-theme the data; weights are shares of pickups and are normalised.

const (
	cityCenterLon = -33.900000 // mid-ocean; nowhere real
	cityCenterLat = 12.400000
)

type borough struct {
	name   string
	code   int
	prefix string // first two characters of every neighborhood code
	ward   int    // base ward number
}

var boroughs = []borough{
	{"", 0, "", 0}, // unknown / outside the city
	{"Thistlemoor", 1, "TM", 3800},
	{"Wyrmstead", 2, "WS", 3700},
	{"Nettleford", 3, "NF", 4000},
	{"Glassharbor", 4, "GH", 4100},
	{"Saltmarrow", 5, "SM", 3900},
}

const (
	boroughUnknown = iota
	boroughThistlemoor
	boroughWyrmstead
	boroughNettleford
	boroughGlassharbor
	boroughSaltmarrow
)

type neighborhood struct {
	name       string
	code       string // 4 characters
	boroughIdx int
	gid        int
	tractLabel string
	tractCode  string
	class      string // "I" or "E"
	ward       int
	weight     float64
	lonMicro   int // centroid, micro-degrees
	latMicro   int
	airport    bool
}

type named struct {
	name    string
	borough int
	weight  float64
	airport bool
}

// The hand-named neighborhoods, in share order; these are the ones that
// ever appear in a top-10 table.
var namedNeighborhoods = []named{
	{"Runewell-Old Quarry", boroughThistlemoor, 17.7, false},
	{"Cinder Row-Tallow Market", boroughThistlemoor, 9.5, false},
	{"Lantern Hill-Hollowgate", boroughThistlemoor, 6.8, false},
	{"Quillbridge-Scribe's Yard", boroughThistlemoor, 6.8, false},
	{"Moonvale Heights-Owlmere", boroughThistlemoor, 6.2, false},
	{"Ashfall Commons-Kiln Row", boroughThistlemoor, 4.9, false},
	{"Skyport", boroughGlassharbor, 4.8, true},
	{"Thornwatch-Southspire", boroughThistlemoor, 4.7, false},
	{"Brackenreach", boroughThistlemoor, 4.6, false},
	{"Glimmerfold", boroughThistlemoor, 4.3, false},
	{"Portstone Landing-Saltgate", boroughThistlemoor, 3.8, false},
	{"Ironholt Square", boroughThistlemoor, 3.6, false},
	{"Emberside", boroughThistlemoor, 3.3, false},
	{"Wardenmoor-Lower Emberwick", boroughThistlemoor, 3.0, false},
	{"Fernwick", boroughThistlemoor, 2.9, false},
	{"Duskmere", boroughThistlemoor, 2.7, false},
	{"Silverbell Court", boroughThistlemoor, 1.8, false},
	{"", boroughUnknown, 1.5, false},
	{"Greenwarren-Old Cemetery-Thistlemoor", boroughThistlemoor, 1.4, false},
	{"Hexwell Heights", boroughThistlemoor, 0.64, false},
	{"Sablecross", boroughThistlemoor, 0.56, false},
	{"Hollowmarket", boroughThistlemoor, 0.53, false},
	{"Tidewrack-Nettleford Landing", boroughNettleford, 0.43, false},
	{"Gantry Fen-Wyvern's Rest-Coalmarsh", boroughGlassharbor, 0.38, false},
	{"Kelpgate-Brinehill-Old Nettleford", boroughNettleford, 0.36, false},
	{"Cauldron Court", boroughThistlemoor, 0.34, false},
	{"Hexwell Ridge", boroughThistlemoor, 0.33, false},
	{"Willowspire", boroughThistlemoor, 0.30, false},
	{"Starling Reach", boroughGlassharbor, 0.24, false},
	{"Fennel Slope-Copperwash", boroughNettleford, 0.19, false},
	{"Mirrorbridge-Ravenwood", boroughGlassharbor, 0.18, false},
	{"Candlewharf-Rope Walk", boroughNettleford, 0.17, false},
	{"Northspire-Pyre Grounds", boroughThistlemoor, 0.15, false},
	{"Nettleford Heights-Cobble Rise", boroughNettleford, 0.13, false},
	{"Yarrow Heights", boroughThistlemoor, 0.12, false},
	{"Wyrmwall East", boroughNettleford, 0.11, false},
	{"Fort Grimwold", boroughNettleford, 0.11, false},
	{"Stonewright Row", boroughGlassharbor, 0.10, false},
	{"Tinkersvale", boroughThistlemoor, 0.10, false},
	{"Wyrmstead South", boroughWyrmstead, 0.09, false},
}

// The long tail is composed from these parts so cardinality is easy to tune.
var (
	tailPrefixes = []string{
		"Ash", "Bracken", "Cinder", "Dusk", "Ember", "Fern", "Glimmer", "Hollow",
		"Iron", "Juniper", "Kestrel", "Lantern", "Moss", "Nettle", "Owl", "Pyre",
		"Quill", "Rune", "Sable", "Thorn", "Umber", "Vale", "Willow", "Yarrow",
	}
	tailCores = []string{
		"well", "gate", "mere", "reach", "holt", "wick", "ford", "moor",
		"spire", "hallow", "bridge", "stead",
	}
	tailSuffixes = []string{
		"Old Kiln", "Tinker's Bend", "Salt Steps", "Wax Alley", "Bell Court",
		"Miller's Green", "Stone Stair", "Copper Lane", "Weaver's Rise",
		"Crow Steps", "Tallow Yard", "Grey Mill", "Fox Hollow", "Ferry Landing",
		"Chapel Row", "Drover's Gate",
	}
	tailBoroughs = []int{ // 20-slot cycle; roughly 55/25/10/5/5 by borough
		boroughThistlemoor, boroughThistlemoor, boroughThistlemoor, boroughThistlemoor,
		boroughThistlemoor, boroughThistlemoor, boroughThistlemoor, boroughThistlemoor,
		boroughThistlemoor, boroughThistlemoor, boroughThistlemoor, boroughGlassharbor,
		boroughGlassharbor, boroughGlassharbor, boroughGlassharbor, boroughGlassharbor,
		boroughNettleford, boroughNettleford, boroughWyrmstead, boroughSaltmarrow,
	}
)

const (
	tailCount       = 150
	tailFirstWeight = 0.085
	tailDecay       = 0.97
)

type weighted struct {
	value  string
	weight float64
}

var (
	cabTypes        = []weighted{{"orange", 70}, {"blue", 22}, {"purple", 8}}
	cabTypesAirport = []weighted{{"orange", 50}, {"blue", 20}, {"purple", 30}}
	vendors         = []weighted{{"1", 47}, {"2", 53}}
	payments        = []weighted{{"CSH", 61.6}, {"CRE", 37.9}, {"NOC", 0.35}, {"DIS", 0.13}}
	passengers      = []weighted{{"1", 70}, {"2", 14}, {"5", 5.5}, {"3", 4.3}, {"6", 3.6}, {"4", 2.2}, {"0", 0.04}, {"7", 0.01}}
)

// table is a cumulative-weight lookup; pick is O(log n) via binary search.
type table struct {
	cum []float64
}

func newTable(weights []float64) table {
	cum := make([]float64, len(weights))
	var s float64
	for i, w := range weights {
		s += w
		cum[i] = s
	}
	for i := range cum {
		cum[i] /= s
	}
	return table{cum: cum}
}

func (t table) pick(u float64) int {
	i := sort.SearchFloat64s(t.cum, u)
	for i < len(t.cum)-1 && t.cum[i] <= u {
		i++
	}
	if i >= len(t.cum) {
		i = len(t.cum) - 1
	}
	return i
}

func weightedTable(w []weighted) table {
	ws := make([]float64, len(w))
	for i := range w {
		ws[i] = w[i].weight
	}
	return newTable(ws)
}

var (
	neighborhoods    []neighborhood
	pickupTable      table
	top3             [3]int
	cabTable         = weightedTable(cabTypes)
	cabAirportTable  = weightedTable(cabTypesAirport)
	vendorTable      = weightedTable(vendors)
	paymentTable     = weightedTable(payments)
	passengerTable   = weightedTable(passengers)
	base36           = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	boroughCounts    = make([]int, len(boroughs))
	nameSeen         = map[string]bool{}
	errDuplicateName = "seedgen: duplicate neighborhood name %q"
)

func init() {
	for _, n := range namedNeighborhoods {
		addNeighborhood(n.name, n.borough, n.weight, n.airport)
	}
	w := tailFirstWeight
	for i, added := 0, 0; added < tailCount; i++ {
		name := tailPrefixes[i%len(tailPrefixes)] + tailCores[(i/len(tailPrefixes))%len(tailCores)]
		if i%3 == 0 {
			name += "-" + tailSuffixes[i%len(tailSuffixes)]
		}
		if nameSeen[name] { // a composed name that collides with a hand-named one
			continue
		}
		addNeighborhood(name, tailBoroughs[added%len(tailBoroughs)], w, false)
		w *= tailDecay
		added++
	}
	weights := make([]float64, len(neighborhoods))
	for i := range neighborhoods {
		weights[i] = neighborhoods[i].weight
	}
	pickupTable = newTable(weights)
	top3 = [3]int{0, 1, 2}
}

func addNeighborhood(name string, boroughIdx int, weight float64, airport bool) {
	if name != "" && nameSeen[name] {
		panic(fmt.Sprintf(errDuplicateName, name))
	}
	nameSeen[name] = true
	b := boroughs[boroughIdx]
	n := neighborhood{name: name, boroughIdx: boroughIdx, weight: weight, airport: airport}
	if boroughIdx != boroughUnknown {
		gid := len(neighborhoods) + 1
		seq := boroughCounts[boroughIdx]
		boroughCounts[boroughIdx]++
		n.gid = gid
		n.code = b.prefix + string(base36[seq/36]) + string(base36[seq%36])
		tract := 100 + gid*3
		n.tractLabel = strconv.Itoa(tract)
		if gid%7 == 0 {
			n.tractLabel += ".02"
		}
		n.tractCode = fmt.Sprintf("%d%06d", b.code, tract*100)
		n.class = "I"
		if gid%7 == 0 || gid%7 == 3 {
			n.class = "E"
		}
		n.ward = b.ward + gid%12
		n.lonMicro = int(cityCenterLon*1e6) + int((unit(uint64(gid), saltCentroid)-0.5)*300000)
		n.latMicro = int(cityCenterLat*1e6) + int((unit(uint64(gid), saltCentroid^0x77)-0.5)*220000)
	} else {
		n.tractLabel = "0"
		n.tractCode = "0000000"
	}
	neighborhoods = append(neighborhoods, n)
}
