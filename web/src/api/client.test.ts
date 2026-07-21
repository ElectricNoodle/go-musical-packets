import { describe, expect, it, vi } from 'vitest'
import { createManagementClient } from './client'
import { configuration } from '../test/fixtures'

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
})
