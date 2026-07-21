import { describe, expect, it, vi } from 'vitest'
import { createManagementClient } from './client'
import { configuration } from '../test/fixtures'
import type { RuleConfig } from './types'

describe('management client', () => {
  it('decodes YAML configuration and preserves the strong revision', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response('instance:\n  id: node\n', {
      status: 200,
      headers: { ETag: '"revision-a"', 'Content-Type': 'application/yaml' },
    }))
    const client = createManagementClient(fetcher)

    const document = await client.getConfig()

    expect(document.revision).toBe('"revision-a"')
    expect(document.config.instance.id).toBe('node')
    expect(fetcher).toHaveBeenCalledWith('/api/v1/config', expect.objectContaining({ cache: 'no-store', credentials: 'same-origin' }))
  })

  it('sends strict YAML updates with the exact ETag precondition', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response('instance:\n  id: studio-node\n', {
      status: 200,
      headers: { ETag: '"revision-b"' },
    }))
    const client = createManagementClient(fetcher)

    await client.updateConfig(configuration, '"revision-a"')

    expect(fetcher).toHaveBeenCalledWith('/api/v1/config', expect.objectContaining({
      method: 'PUT',
      headers: expect.objectContaining({ 'Content-Type': 'application/yaml', 'If-Match': '"revision-a"' }),
      body: expect.stringContaining('default_state: monitor'),
    }))
  })

  it('loads, stages, and discards the next-start configuration by strong revision', async () => {
    const response = (revision: string) => new Response('instance:\n  id: node\n', {
      status: 200, headers: { ETag: revision, 'Content-Type': 'application/yaml' },
    })
    const fetcher = vi.fn()
      .mockResolvedValueOnce(response('"pending-a"'))
      .mockResolvedValueOnce(response('"pending-b"'))
      .mockResolvedValueOnce(response('"active-a"'))
    const client = createManagementClient(fetcher)

    await client.getPendingConfig()
    await client.stageConfig(configuration, '"active-a"')
    await client.cancelPendingConfig('"pending-b"')

    expect(fetcher).toHaveBeenNthCalledWith(1, '/api/v1/config/pending', expect.objectContaining({ cache: 'no-store' }))
    expect(fetcher).toHaveBeenNthCalledWith(2, '/api/v1/config/pending', expect.objectContaining({
      method: 'PUT', headers: expect.objectContaining({ 'If-Match': '"active-a"' }), body: expect.stringContaining('interface: auto'),
    }))
    expect(fetcher).toHaveBeenNthCalledWith(3, '/api/v1/config/pending', expect.objectContaining({
      method: 'DELETE', headers: { 'If-Match': '"pending-b"' },
    }))
  })

  it('exposes bounded problem details from failed requests', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      status: 409,
      code: 'restart_required',
      detail: 'configuration changes require a process restart',
      fields: ['capture.interface'],
    }), { status: 409, headers: { 'Content-Type': 'application/problem+json' } }))
    const client = createManagementClient(fetcher)

    await expect(client.validateConfig(configuration)).rejects.toMatchObject({
      status: 409,
      code: 'restart_required',
      fields: ['capture.interface'],
    })
  })

  it('normalizes nullable validation collections from older runtimes', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      revision: 'revision-a', hot_fields: null, restart_required_fields: null,
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    const client = createManagementClient(fetcher)

    await expect(client.validateConfig(configuration)).resolves.toEqual({
      revision: 'revision-a', hot_fields: [], restart_required_fields: [],
    })
  })

  it('loads bounded flows and sends complete overlay replacements', async () => {
    const fetcher = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        flows: [], overlay: { muted: [], soloed: [] }, total: 0, limit: 250, truncated: false,
      }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ muted: ['flow-a'], soloed: [] }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      }))
    const client = createManagementClient(fetcher)

    await client.getFlows(250)
    await client.setMutedFlows(['flow-a'])

    expect(fetcher).toHaveBeenNthCalledWith(1, '/api/v1/flows?limit=250', expect.objectContaining({ cache: 'no-store' }))
    expect(fetcher).toHaveBeenNthCalledWith(2, '/api/v1/flows/mute', expect.objectContaining({
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ flow_ids: ['flow-a'] }),
    }))
  })

  it('preserves the rules ETag and uses it for optimistic creation', async () => {
    const listed = { revision: 'rules-a', writable: true, rules: [] }
    const created = { revision: 'rules-b', writable: true, rules: [{ id: 'web' }] }
    const fetcher = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(listed), {
        status: 200, headers: { ETag: '"rules-a"', 'Content-Type': 'application/json' },
      }))
      .mockResolvedValueOnce(new Response(JSON.stringify(created), {
        status: 201, headers: { ETag: '"rules-b"', 'Content-Type': 'application/json' },
      }))
    const client = createManagementClient(fetcher)
    const rule = {
      id: 'web', name: 'Web', enabled: true,
      match: { protocol: 'tcp' as const },
      action: { state: 'play' as const, channel: 4 },
    }

    const document = await client.getRules()
    const result = await client.createRule(rule, document.etag)

    expect(document.etag).toBe('"rules-a"')
    expect(result.etag).toBe('"rules-b"')
    expect(fetcher).toHaveBeenNthCalledWith(2, '/api/v1/rules', expect.objectContaining({
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'If-Match': '"rules-a"' },
      body: JSON.stringify(rule),
    }))
  })

  it('sends every ordered-rule mutation with the exact revision', async () => {
    const rule: RuleConfig = {
      id: '.', name: 'Dot', enabled: true,
      match: { protocol: 'tcp' },
      action: { state: 'monitor', channel: 0 },
    }
    const response = () => new Response(JSON.stringify({ revision: 'rules-b', writable: true, rules: [rule] }), {
      status: 200, headers: { ETag: '"rules-b"', 'Content-Type': 'application/json' },
    })
    const fetcher = vi.fn()
      .mockImplementationOnce(async () => response())
      .mockImplementationOnce(async () => response())
      .mockImplementationOnce(async () => response())
      .mockImplementationOnce(async () => response())
    const client = createManagementClient(fetcher)

    await client.replaceRules([rule], '"rules-a"')
    await client.replaceRule('.', rule, '"rules-b"')
    await client.deleteRule('..', '"rules-c"')
    await client.reorderRules(['.'], '"rules-d"')

    expect(fetcher).toHaveBeenNthCalledWith(1, '/api/v1/rules', expect.objectContaining({
      method: 'PUT', headers: { 'Content-Type': 'application/json', 'If-Match': '"rules-a"' }, body: JSON.stringify({ rules: [rule] }),
    }))
    expect(fetcher).toHaveBeenNthCalledWith(2, '/api/v1/rules/%2E', expect.objectContaining({
      method: 'PUT', headers: { 'Content-Type': 'application/json', 'If-Match': '"rules-b"' }, body: JSON.stringify(rule),
    }))
    expect(fetcher).toHaveBeenNthCalledWith(3, '/api/v1/rules/%2E%2E', expect.objectContaining({
      method: 'DELETE', headers: { 'If-Match': '"rules-c"' },
    }))
    expect(fetcher).toHaveBeenNthCalledWith(4, '/api/v1/rules', expect.objectContaining({
      method: 'PATCH', headers: { 'Content-Type': 'application/json', 'If-Match': '"rules-d"' }, body: JSON.stringify({ order: ['.'] }),
    }))
  })
})
