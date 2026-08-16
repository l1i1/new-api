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
import type { AffinityRule } from './types'

// Full upstream Codex session-routing header list, exported as the reference
// for restoring upstream parity. The default template ships the trimmed
// production set (CODEX_CLI_HEADER_PASSTHROUGH_HEADERS_ACTIVE) instead.
// Keep in sync with upstream Codex request headers:
// https://github.com/openai/codex/commit/7c7b4861d88960f7e3bd5b7f30f8351be666dd84
// https://github.com/openai/codex/commit/14df0e8833aad0d6d78287954b61ffac67af936c
// https://github.com/openai/codex/commit/ebdd8795e924a8149b616e46ca2ed7848c207a4b
export const CODEX_CLI_HEADER_PASSTHROUGH_HEADERS = [
  'Originator',
  'Session_id',
  'Thread_id',
  'Session-Id',
  'Thread-Id',
  'X-Client-Request-Id',
  'User-Agent',
  'X-Codex-Beta-Features',
  'X-Codex-Turn-State',
  'X-Codex-Turn-Metadata',
  'X-Codex-Window-Id',
  'X-Codex-Parent-Thread-Id',
  // 'X-Codex-Installation-Id',
  'X-OpenAI-Subagent',
  'X-OpenAI-Memgen-Request',
  // 'X-OAI-Attestation',
  'X-ResponsesAPI-Include-Timing-Metrics',
  'X-OpenAI-Internal-Codex-Responses-Lite',
]

// Header set shipped by the default "codex cli trace" template. Matches the
// Tokeness production rules: only headers proven safe to pass upstream.
const CODEX_CLI_HEADER_PASSTHROUGH_HEADERS_ACTIVE = [
  'Originator',
  'Session_id',
  'User-Agent',
  'X-Codex-Beta-Features',
  'X-Codex-Turn-Metadata',
]

const CLAUDE_CLI_HEADER_PASSTHROUGH_HEADERS = [
  'X-Stainless-Arch',
  'X-Stainless-Lang',
  'X-Stainless-Os',
  'X-Stainless-Package-Version',
  'X-Stainless-Retry-Count',
  'X-Stainless-Runtime',
  'X-Stainless-Runtime-Version',
  'X-Stainless-Timeout',
  'User-Agent',
  'X-App',
  'Anthropic-Beta',
  'Anthropic-Dangerous-Direct-Browser-Access',
  'Anthropic-Version',
]

function buildPassHeadersTemplate(headers: string[]) {
  return {
    operations: [
      {
        mode: 'pass_headers',
        value: [...headers],
        keep_origin: true,
      },
    ],
  }
}

function buildCodexPassHeadersTemplate() {
  return {
    operations: [
      {
        mode: 'pass_headers',
        value: [...CODEX_CLI_HEADER_PASSTHROUGH_HEADERS_ACTIVE],
        keep_origin: true,
      },
    ],
  }
}

export type RuleTemplate = Omit<AffinityRule, 'id'>

export const RULE_TEMPLATES: Record<string, RuleTemplate> = {
  codexCli: {
    name: 'codex cli trace',
    model_regex: ['^gpt-.*$'],
    path_regex: ['/v1/responses'],
    key_sources: [{ type: 'gjson', path: 'prompt_cache_key' }],
    param_override_template: buildCodexPassHeadersTemplate(),
    value_regex: '',
    ttl_seconds: 0,
    skip_retry_on_failure: true,
    include_using_group: true,
    include_model_name: false,
    include_rule_name: true,
    user_agent_include: [],
  },
  claudeCli: {
    name: 'claude cli trace',
    model_regex: ['^claude-.*$'],
    path_regex: ['/v1/messages'],
    key_sources: [{ type: 'gjson', path: 'metadata.user_id' }],
    param_override_template: buildPassHeadersTemplate(
      CLAUDE_CLI_HEADER_PASSTHROUGH_HEADERS
    ),
    value_regex: '',
    ttl_seconds: 0,
    skip_retry_on_failure: true,
    include_using_group: true,
    include_model_name: false,
    include_rule_name: true,
    user_agent_include: [],
  },
  chatCompletionAffinity: {
    name: 'chat completion affinity',
    model_regex: ['.*'],
    path_regex: ['^/v1/chat/completions', '^/pg/chat/completions'],
    key_sources: [
      { type: 'gjson', path: 'metadata.user_id' },
      { type: 'gjson', path: 'user' },
      { type: 'context_int', key: 'token_id' },
      { type: 'context_int', key: 'id' },
    ],
    value_regex: '',
    ttl_seconds: 0,
    skip_retry_on_failure: false,
    include_using_group: true,
    include_model_name: true,
    include_rule_name: true,
    user_agent_include: [],
  },
  responsesTrace: {
    name: 'responses trace',
    model_regex: ['.*'],
    path_regex: ['^/v1/responses'],
    key_sources: [
      { type: 'gjson', path: 'prompt_cache_key' },
      { type: 'context_int', key: 'token_id' },
      { type: 'context_int', key: 'id' },
    ],
    value_regex: '',
    ttl_seconds: 0,
    skip_retry_on_failure: false,
    include_using_group: true,
    include_model_name: true,
    include_rule_name: true,
    user_agent_include: [],
  },
  messagesTrace: {
    name: 'messages trace',
    model_regex: ['.*'],
    path_regex: ['^/v1/messages'],
    key_sources: [
      { type: 'gjson', path: 'metadata.user_id' },
      { type: 'context_int', key: 'token_id' },
      { type: 'context_int', key: 'id' },
    ],
    value_regex: '',
    ttl_seconds: 0,
    skip_retry_on_failure: false,
    include_using_group: true,
    include_model_name: true,
    include_rule_name: true,
    user_agent_include: [],
  },
  geminiNativeAffinity: {
    name: 'gemini native affinity',
    model_regex: ['.*'],
    path_regex: ['^/v1beta/models/', '^/v1/models/'],
    key_sources: [
      { type: 'context_int', key: 'token_id' },
      { type: 'context_int', key: 'id' },
    ],
    value_regex: '',
    ttl_seconds: 0,
    skip_retry_on_failure: false,
    include_using_group: true,
    include_model_name: true,
    include_rule_name: true,
    user_agent_include: [],
  },
}

export function makeUniqueName(
  existingNames: Set<string>,
  baseName: string
): string {
  const base = (baseName || '').trim() || 'rule'
  if (!existingNames.has(base)) return base
  for (let i = 2; i < 1000; i++) {
    const n = `${base}-${i}`
    if (!existingNames.has(n)) return n
  }
  return `${base}-${Date.now()}`
}

export function cloneTemplate<T>(template: T): T {
  return JSON.parse(JSON.stringify(template))
}
