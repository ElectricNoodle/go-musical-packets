import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { PeersDocument } from '../api/types'
import { stubClient } from '../test/fixtures'
import { PeersWorkspace } from './PeersWorkspace'

const edge: PeersDocument = {
  role: 'edge',
  outbound: {
    enabled: true, target: 'wss://host.example/api/v1/peer', remote_instance: 'host-1', state: 'connected',
    protocol_version: 'peer-v1', mapping_version: 'flow-mode-v1', queue: { depth: 3, capacity: 128 },
    sent_total: 240, dropped_full: 2, dropped_stale: 4, reconnects: 1, send_rate: 3.5,
    connected_at: '2026-07-22T10:00:00Z', last_sent_at: '2026-07-22T10:00:10Z', rtt_ms: 18,
    active_channels: [2, 13],
  },
  nodes: [],
}

const host: PeersDocument = {
  role: 'host',
  nodes: [{
    instance_id: 'edge-kitchen', remote_address: '192.0.2.4:53000', state: 'connected', authenticated: true,
    protocol_version: 'peer-v1', mapping_version: 'flow-mode-v1', connected_at: '2026-07-22T10:00:00Z',
    last_seen_at: '2026-07-22T10:00:10Z', note_rate: 2.4, received_total: 11, accepted_total: 10,
    rejected_total: 1, duplicate_total: 0, stale_total: 0, active_channels: [4, 9],
  }, {
    instance_id: 'edge-studio', remote_address: '192.0.2.5:53001', state: 'disconnected', authenticated: true,
    protocol_version: 'peer-v1', mapping_version: 'flow-mode-v1', connected_at: '2026-07-22T09:00:00Z',
    disconnected_at: '2026-07-22T09:30:00Z', last_seen_at: '2026-07-22T09:30:00Z', note_rate: 0,
    received_total: 8, accepted_total: 8, rejected_total: 0, duplicate_total: 0, stale_total: 0, active_channels: [1],
  }],
}

describe('peer workspace', () => {
  it('shows an edge destination, bounded queue, delivery totals, and channels', async () => {
    const client = stubClient({ getPeers: vi.fn().mockResolvedValue(edge) })
    render(<PeersWorkspace client={client} />)

    expect(await screen.findByRole('heading', { name: 'host-1' })).toBeInTheDocument()
    expect(screen.getByText('wss://host.example/api/v1/peer')).toBeInTheDocument()
    expect(screen.getByText('3 / 128')).toBeInTheDocument()
    expect(screen.getByText('240')).toBeInTheDocument()
    expect(screen.getByText('CH 13')).toBeInTheDocument()
    expect(screen.queryByText(/token/i)).not.toBeInTheDocument()
  })

  it('shows, searches, and links connected host nodes into the viewer', async () => {
    const client = stubClient({ getPeers: vi.fn().mockResolvedValue(host) })
    const user = userEvent.setup()
    render(<PeersWorkspace client={client} />)

    const kitchen = await screen.findByRole('heading', { name: 'edge-kitchen' })
    const card = kitchen.closest('article')
    expect(card).not.toBeNull()
    if (!card) throw new Error('expected node card')
    expect(within(card).getByText('Bearer authenticated')).toBeInTheDocument()
    expect(within(card).getByText('CH 09')).toBeInTheDocument()
    expect(within(card).getByRole('link', { name: /view notes/i })).toHaveAttribute('href', '/viewer?origin=edge-kitchen')

    await user.type(screen.getByRole('searchbox', { name: /search nodes/i }), 'studio')
    expect(screen.getByRole('heading', { name: 'edge-studio' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'edge-kitchen' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /refresh now/i }))
    await waitFor(() => expect(client.getPeers).toHaveBeenCalledTimes(2))
  })
})
