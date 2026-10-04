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

package geo

import "strings"

var continents = [...]Code2{{'A', 'F'}, {'A', 'N'}, {'A', 'S'}, {'E', 'U'}, {'N', 'A'}, {'O', 'C'}, {'S', 'A'}}

const assignedCountries = `
AD:EU AE:AS AF:AS AG:NA AI:NA AL:EU AM:AS AO:AF AQ:AN AR:SA AS:OC AT:EU AU:OC AW:NA AX:EU AZ:AS
BA:EU BB:NA BD:AS BE:EU BF:AF BG:EU BH:AS BI:AF BJ:AF BL:NA BM:NA BN:AS BO:SA BQ:NA BR:SA BS:NA BT:AS BV:AN BW:AF
BY:EU BZ:NA
CA:NA CC:AS CD:AF CF:AF CG:AF CH:EU CI:AF CK:OC CL:SA CM:AF CN:AS CO:SA CR:NA CU:NA CV:AF CW:NA CX:OC CY:EU CZ:EU
DE:EU DJ:AF DK:EU DM:NA DO:NA DZ:AF
EC:SA EE:EU EG:AF EH:AF ER:AF ES:EU ET:AF
FI:EU FJ:OC FK:SA FM:OC FO:EU FR:EU
GA:AF GB:EU GD:NA GE:AS GF:SA GG:EU GH:AF GI:EU GL:NA GM:AF GN:AF GP:NA GQ:AF GR:EU GS:AN GT:NA GU:OC GW:AF GY:SA
HK:AS HM:AN HN:NA HR:EU HT:NA HU:EU
ID:AS IE:EU IL:AS IM:EU IN:AS IO:AS IQ:AS IR:AS IS:EU IT:EU
JE:EU JM:NA JO:AS JP:AS
KE:AF KG:AS KH:AS KI:OC KM:AF KN:NA KP:AS KR:AS KW:AS KY:NA KZ:AS
LA:AS LB:AS LC:NA LI:EU LK:AS LR:AF LS:AF LT:EU LU:EU LV:EU LY:AF
MA:AF MC:EU MD:EU ME:EU MF:NA MG:AF MH:OC MK:EU ML:AF MM:AS MN:AS MO:AS MP:OC MQ:NA MR:AF MS:NA MT:EU MU:AF
MV:AS MW:AF MX:NA MY:AS MZ:AF
NA:AF NC:OC NE:AF NF:OC NG:AF NI:NA NL:EU NO:EU NP:AS NR:OC NU:OC NZ:OC
OM:AS
PA:NA PE:SA PF:OC PG:OC PH:AS PK:AS PL:EU PM:NA PN:OC PR:NA PS:AS PT:EU PW:OC PY:SA
QA:AS
RE:AF RO:EU RS:EU RU:EU RW:AF
SA:AS SB:OC SC:AF SD:AF SE:EU SG:AS SH:AF SI:EU SJ:EU SK:EU SL:AF SM:EU SN:AF SO:AF SR:SA SS:AF ST:AF SV:NA
SX:NA SY:AS SZ:AF
TC:NA TD:AF TF:AN TG:AF TH:AS TJ:AS TK:OC TL:OC TM:AS TN:AF TO:OC TR:AS TT:NA TV:OC TW:AS TZ:AF
UA:EU UG:AF UM:OC US:NA UY:SA UZ:AS
VA:EU VC:NA VE:SA VG:NA VI:NA VN:AS VU:OC
WF:OC WS:OC
XK:EU
YE:AS YT:AF
ZA:AF ZM:AF ZW:AF
`

var countryContinents [26 * 26]uint8 // by two-letter code: its continent's position in continents, or 0

var continentBits [26 * 26]uint8 // by two-letter code: its bit in a List's continent mask, or 0

var commonMistakes = map[Code2]string{ // codes easily mistaken for assigned countries, and the code meant
	{'U', 'K'}: "GB",
	{'E', 'L'}: "GR",
	{'E', 'U'}: continentPrefix + "EU",
	{'O', 'C'}: continentPrefix + "OC",
	{'A', 'N'}: continentPrefix + "AN",
}

func init() {
	// continents are in the order of their bits in a List's continent mask
	for i, c := range continents {
		continentBits[c.index()] = 1 << i
	}
	// every assigned ISO 3166-1 code, and XK, which the databases use for Kosovo, on the databases' continent
	for pair := range strings.FieldsSeq(assignedCountries) {
		country, _ := ParseCode2(pair[:2])
		continent, _ := ParseCode2(pair[3:])
		countryContinents[country.index()] = continentOrdinal(continent)
	}
}

// ParseCode2 returns the two ASCII letters of s as an upper-case Code2, ignoring their case
func ParseCode2(s string) (Code2, bool) {
	if len(s) != 2 {
		return Code2{}, false
	}
	var c Code2
	for i := range 2 {
		b := s[i]
		if b >= 'a' && b <= 'z' {
			b -= 'a' - 'A'
		}
		if b < 'A' || b > 'Z' {
			return Code2{}, false
		}
		c[i] = b
	}
	return c, true
}

func (c Code2) index() int {
	return int(c[0]-'A')*26 + int(c[1]-'A')
}

func (c Code2) isLetters() bool {
	return c[0] >= 'A' && c[0] <= 'Z' && c[1] >= 'A' && c[1] <= 'Z'
}

// IsCountry reports whether c is an assigned country code
func IsCountry(c Code2) bool {
	return c.isLetters() && countryContinents[c.index()] != 0
}

// ContinentOf returns the continent the location databases place the country on, or the zero
// Code2 when c is not an assigned country
func ContinentOf(c Code2) Code2 {
	if !c.isLetters() {
		return Code2{}
	}
	if i := countryContinents[c.index()]; i != 0 {
		return continents[i-1]
	}
	return Code2{}
}

// IsContinent reports whether c is a continent code
func IsContinent(c Code2) bool {
	return continentBit(c) != 0
}

func continentBit(c Code2) uint8 {
	if !c.isLetters() {
		return 0
	}
	return continentBits[c.index()]
}

func continentOrdinal(c Code2) uint8 {
	var n uint8
	for _, k := range continents {
		n++
		if k == c {
			return n
		}
	}
	return 0
}
