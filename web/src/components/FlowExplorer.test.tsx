import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { FlowExplorer } from './FlowExplorer'
import { flowPage, stubClient } from '../test/fixtures'

describe('flow explorer', () => {
  it('loads a bounded flow page and filters canonical endpoints', async () => {
    const client = stubClient()
    const user = userEvent.setup()
    render(<FlowExplorer client={client} announce={vi.fn()} />)

    expect(await screen.findByText('0123456789abcdef01234567')).toBeInTheDocument()
    expect(client.getFlows).toHaveBeenCalledWith(500, expect.any(AbortSignal))

    await user.type(screen.getByRole('searchbox', { name: /search flows/i }), '5353')

    expect(screen.getByText('fedcba9876543210fedcba98')).toBeInTheDocument()
    expect(screen.queryByText('0123456789abcdef01234567')).not.toBeInTheDocument()
  })

  it('sorts rows and replaces the complete mute set', async () => {
    const client = stubClient()
    const announce = vi.fn()
    const user = userEvent.setup()
    render(<FlowExplorer client={client} announce={announce} />)
    await screen.findByText('0123456789abcdef01234567')

    await user.click(screen.getByRole('button', { name: 'Packets' }))
    const rows = screen.getAllByRole('row')
    const firstDataRow = rows.at(1)
    expect(firstDataRow).toBeDefined()
    if (!firstDataRow) throw new Error('expected a flow row')
    expect(within(firstDataRow).getByText('240')).toBeInTheDocument()

    await user.click(within(firstDataRow).getByRole('button', { name: 'Mute' }))
    await waitFor(() => expect(client.setMutedFlows).toHaveBeenCalledWith([
      '0123456789abcdef01234567',
      'fedcba9876543210fedcba98',
    ]))
    expect(announce).toHaveBeenCalledWith('Mute state updated for 2 flows.', 'success')
  })

  it('selects visible traffic and solos it as one authoritative set', async () => {
    const client = stubClient({
      getFlows: vi.fn().mockResolvedValue({ ...flowPage, flows: [...flowPage.flows] }),
    })
    const user = userEvent.setup()
    render(<FlowExplorer client={client} announce={vi.fn()} />)
    await screen.findByText('0123456789abcdef01234567')

    await user.type(screen.getByRole('searchbox', { name: /search flows/i }), 'tcp')
    await user.click(screen.getByRole('checkbox', { name: /select all visible/i }))
    await user.click(screen.getByRole('button', { name: /solo selected/i }))

    await waitFor(() => expect(client.setSoloedFlows).toHaveBeenCalledWith(['0123456789abcdef01234567']))
  })
})
