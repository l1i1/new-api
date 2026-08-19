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
import { describe, expect, test } from 'vitest'

import type { ChatCompletionRequest } from '../types'
import { createStreamRequestController } from './use-stream-request'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((promiseResolve) => {
    resolve = promiseResolve
  })
  return { promise, resolve }
}

class FakeStreamSource {
  readyState = 0
  closed = false
  streamed = false
  private listeners = new Map<
    string,
    Array<(event: Event & { data?: string; readyState?: number }) => void>
  >()

  addEventListener(
    type: string,
    listener: (event: Event & { data?: string; readyState?: number }) => void
  ) {
    const listeners = this.listeners.get(type) ?? []
    listeners.push(listener)
    this.listeners.set(type, listeners)
  }

  close() {
    this.closed = true
  }

  stream() {
    this.streamed = true
  }

  emit(type: string, data?: string) {
    for (const listener of this.listeners.get(type) ?? []) {
      listener({ data, readyState: this.readyState } as Event & {
        data?: string
        readyState?: number
      })
    }
  }
}

const payload: ChatCompletionRequest = {
  model: 'test-model',
  messages: [{ role: 'user', content: 'hello' }],
  stream: true,
}

const noopCallbacks = {
  onUpdate: () => undefined,
  onComplete: () => undefined,
  onError: () => undefined,
}

describe('latest-wins stream request coordination', () => {
  test('only creates a stream for the latest header request', async () => {
    const firstHeaders = deferred<Record<string, string>>()
    const secondHeaders = deferred<Record<string, string>>()
    let headerRequest = 0
    const sources: FakeStreamSource[] = []
    const controller = createStreamRequestController({
      getHeaders: () => {
        headerRequest += 1
        return headerRequest === 1
          ? firstHeaders.promise
          : secondHeaders.promise
      },
      createSource: () => {
        const source = new FakeStreamSource()
        sources.push(source)
        return source
      },
      setStreaming: () => undefined,
    })

    const first = controller.send(payload, noopCallbacks)
    const second = controller.send(payload, noopCallbacks)
    firstHeaders.resolve({ Authorization: 'Bearer stale' })
    await first
    expect(sources.length).toBe(0)

    secondHeaders.resolve({ Authorization: 'Bearer current' })
    await second
    expect(sources.length).toBe(1)
    expect(sources[0]?.streamed).toBe(true)
  })

  test('stop cancels a request that is still waiting for headers', async () => {
    const headers = deferred<Record<string, string>>()
    let sourceCount = 0
    const controller = createStreamRequestController({
      getHeaders: () => headers.promise,
      createSource: () => {
        sourceCount += 1
        return new FakeStreamSource()
      },
      setStreaming: () => undefined,
    })

    const request = controller.send(payload, noopCallbacks)
    controller.stop()
    headers.resolve({ Authorization: 'Bearer ignored' })
    await request

    expect(sourceCount).toBe(0)
  })

  test('recovers from synchronous source construction failure', async () => {
    const errors: string[] = []
    const streamingStates: boolean[] = []
    let attempts = 0
    let recoveredSource: FakeStreamSource | undefined
    const controller = createStreamRequestController({
      getHeaders: () => Promise.resolve({ Authorization: 'Bearer test' }),
      createSource: () => {
        attempts += 1
        if (attempts === 1) {
          throw new Error('source construction failed')
        }
        recoveredSource = new FakeStreamSource()
        return recoveredSource
      },
      setStreaming: (streaming) => streamingStates.push(streaming),
    })
    const callbacks = {
      onUpdate: () => undefined,
      onComplete: () => undefined,
      onError: (error: string) => errors.push(error),
    }

    await assert.doesNotReject(() => controller.send(payload, callbacks))
    assert.deepEqual(errors, ['source construction failed'])
    assert.equal(streamingStates.at(-1), false)

    await assert.doesNotReject(() => controller.send(payload, callbacks))
    assert.equal(recoveredSource?.streamed, true)
    controller.dispose()
  })

  test('dispose cancels a pending header request without a state update', async () => {
    const headers = deferred<Record<string, string>>()
    const streamingStates: boolean[] = []
    let sourceCount = 0
    const controller = createStreamRequestController({
      getHeaders: () => headers.promise,
      createSource: () => {
        sourceCount += 1
        return new FakeStreamSource()
      },
      setStreaming: (streaming) => streamingStates.push(streaming),
    })

    const request = controller.send(payload, noopCallbacks)
    controller.dispose()
    headers.resolve({ Authorization: 'Bearer ignored' })
    await request

    expect(sourceCount).toBe(0)
    expect(streamingStates).toEqual([false])
  })

  test('closes the previous source and ignores all of its later events', async () => {
    const nextHeaders = deferred<Record<string, string>>()
    let headerRequest = 0
    const sources: FakeStreamSource[] = []
    const updates: string[] = []
    const controller = createStreamRequestController({
      getHeaders: () => {
        headerRequest += 1
        if (headerRequest === 1) {
          return Promise.resolve({ Authorization: 'Bearer first' })
        }
        return nextHeaders.promise
      },
      createSource: () => {
        const source = new FakeStreamSource()
        sources.push(source)
        return source
      },
      setStreaming: () => undefined,
    })
    const callbacks = {
      onUpdate: (_type: 'reasoning' | 'content', chunk: string) =>
        updates.push(chunk),
      onComplete: () => undefined,
      onError: () => undefined,
    }

    await controller.send(payload, callbacks)
    const second = controller.send(payload, callbacks)
    expect(sources[0]?.closed).toBe(true)
    sources[0]?.emit(
      'message',
      JSON.stringify({ choices: [{ delta: { content: 'stale' } }] })
    )

    nextHeaders.resolve({ Authorization: 'Bearer second' })
    await second
    sources[1]?.emit(
      'message',
      JSON.stringify({ choices: [{ delta: { content: 'current' } }] })
    )

    expect(updates).toEqual(['current'])
  })

  test('keeps streams for separate sessions independent', async () => {
    const sources: FakeStreamSource[] = []
    const updates: string[] = []
    const controller = createStreamRequestController({
      getHeaders: () => Promise.resolve({ Authorization: 'Bearer test' }),
      createSource: () => {
        const source = new FakeStreamSource()
        sources.push(source)
        return source
      },
      setStreaming: () => undefined,
    })

    await controller.send(
      payload,
      {
        onUpdate: (_type, chunk) => updates.push(`first:${chunk}`),
        onComplete: () => undefined,
        onError: () => undefined,
      },
      'first-session'
    )
    await controller.send(
      payload,
      {
        onUpdate: (_type, chunk) => updates.push(`second:${chunk}`),
        onComplete: () => undefined,
        onError: () => undefined,
      },
      'second-session'
    )

    sources[0]?.emit(
      'message',
      JSON.stringify({ choices: [{ delta: { content: 'one' } }] })
    )
    sources[1]?.emit(
      'message',
      JSON.stringify({ choices: [{ delta: { content: 'two' } }] })
    )

    assert.equal(sources[0]?.closed, false)
    assert.equal(sources[1]?.closed, false)
    assert.deepEqual(updates, ['first:one', 'second:two'])
  })

  test('fails and clears streaming when a successful response closes without DONE', async () => {
    const source = new FakeStreamSource()
    const errors: string[] = []
    const streamingStates: boolean[] = []
    const controller = createStreamRequestController({
      getHeaders: () => Promise.resolve({ Authorization: 'Bearer test' }),
      createSource: () => source,
      setStreaming: (streaming) => streamingStates.push(streaming),
    })

    await controller.send(payload, {
      onUpdate: () => undefined,
      onComplete: () => undefined,
      onError: (error) => errors.push(error),
    })
    source.readyState = 2
    source.emit('readystatechange')

    assert.deepEqual(errors, ['Connection closed'])
    assert.equal(source.closed, true)
    assert.equal(streamingStates.at(-1), false)
  })
})
