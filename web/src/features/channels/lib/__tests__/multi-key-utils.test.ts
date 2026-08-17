/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
  aggregateMultiKeyObservability,
  formatMultiKeyTestResult,
  getMultiKeyIndex,
  getMultiKeyTestResult,
} from '../multi-key-utils'

function observabilityMetric(
  requestedModel: string,
  sampleCoverage: number,
  usageCoverage: number
) {
  return {
    channel_id: 1,
    credential_id: 17,
    requested_model: requestedModel,
    group: 'default',
    protocol: 'openai',
    request_count: 10,
    attempt_count: 10,
    request_success_rate: 100,
    attempt_success_rate: 100,
    cache_hit_rate: 50,
    cache_token_rate: 0,
    avg_latency_ms: 100,
    p95_latency_ms: 200,
    avg_request_latency_ms: 100,
    p95_request_latency_ms: 200,
    avg_ttft_ms: 50,
    p95_ttft_ms: 80,
    avg_upstream_frt_ms: 25,
    p95_upstream_frt_ms: 40,
    sample_coverage: sampleCoverage,
    usage_coverage: usageCoverage,
    sample_sufficient: false,
    usage_sufficient: false,
  }
}

describe('multi-key index normalization', () => {
  test('uses the API position when legacy index is missing', () => {
    assert.equal(getMultiKeyIndex({ index: Number.NaN, position: 4 }), 4)
  })

  test('falls back to the row index for malformed identity fields', () => {
    assert.equal(getMultiKeyIndex({ index: Number.NaN }, 7), 7)
  })
})

describe('multi-key test result formatting', () => {
  test('does not fall back to a legacy index when a credential id is present', () => {
    const legacyResult = {
      credential_id: 3,
      index: 3,
      fingerprint: 'legacy',
      status: 'failed',
      http_status: 401,
      latency_ms: 25,
    }

    assert.equal(
      getMultiKeyTestResult(
        { 3: legacyResult },
        {
          credential_id: 17,
          index: 3,
          status: 1,
        }
      ),
      undefined
    )
  })

  test('uses the legacy index only when no credential id is available', () => {
    const legacyResult = {
      credential_id: 3,
      index: 3,
      fingerprint: 'legacy',
      status: 'failed',
      http_status: 401,
      latency_ms: 25,
    }

    assert.equal(
      getMultiKeyTestResult(
        { 3: legacyResult },
        {
          index: 3,
          status: 1,
        }
      ),
      legacyResult
    )
  })

  test('renders persisted HTTP status and error message after a reload', () => {
    const formatted = formatMultiKeyTestResult(undefined, {
      index: 0,
      status: 1,
      last_test_status: 'failed',
      last_test_http_status: 429,
      last_test_error_message: 'upstream rate limit exceeded',
    })

    assert.equal(
      formatted,
      'Failed · HTTP status: 429 · upstream rate limit exceeded'
    )
  })

  test('prefers the live task result associated with the credential', () => {
    const formatted = formatMultiKeyTestResult(
      {
        credential_id: 17,
        index: 3,
        fingerprint: 'fingerprint',
        status: 'failed',
        http_status: 401,
        latency_ms: 25,
        error_code: 'authentication',
      },
      undefined
    )

    assert.equal(formatted, 'Failed · HTTP status: 401 · authentication')
  })
})

describe('multi-key observability aggregation', () => {
  test('marks combined model samples sufficient after the credential total reaches the threshold', () => {
    const metrics = aggregateMultiKeyObservability([
      observabilityMetric('model-a', 100, 100),
      observabilityMetric('model-b', 100, 100),
    ])

    assert.equal(metrics[17]?.request_count, 20)
    assert.equal(metrics[17]?.sample_sufficient, true)
    assert.equal(metrics[17]?.usage_sufficient, true)
  })
})
