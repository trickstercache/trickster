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

const testTOMLNoALB = `
backends:
  test:
 `

const testTOMLBadOutputFormat1 = `
backends:
  test:
    alb:
      mechanism: 'not-tsm'
      output_format: invalid
 `

const testTOMLBadOutputFormat2 = `
backends:
  test:
    alb:
      mechanism: tsm
      output_format: invalid
`

const testTOML = `
backends:
  test:
    alb:
      mechanism: tsm
      output_format: prometheus
      healthy_floor: 1
      pool: [ 'test' ]
`

const testFGR = `
backends:
  test:
    alb:
      mechanism: fgr
      fgr_status_codes: [200, 201]
`

const testFGRNoCodes = `
backends:
  test:
    alb:
      mechanism: fgr
`
