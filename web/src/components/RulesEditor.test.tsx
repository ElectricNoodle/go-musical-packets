import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { RulesDocument } from '../api/types'
import { flowPage, stubClient } from '../test/fixtures'
import { RulesEditor } from './RulesEditor'

const orderedRules: RulesDocument = {
  revision: 'rules-a',
  etag: '"rules-a"',
  writable: true,
  rules: [
    {
      id: 'all-tcp', name: 'All TCP', enabled: true,
      match: { protocol: 'tcp' },
      action: { state: 'monitor', channel: 0 },
    },
    {
      id: 'web-traffic', name: 'Web traffic', enabled: true,
      match: { protocol: 'tcp', destination_ports: { minimum: 443, maximum: 443 } },
      action: { state: 'play', channel: 4 },
    },
  ],
}

function cardFor(id: string) {
  const card = screen.getByText(id).closest('article')
  if (!card) throw new Error(`expected rule card ${id}`)
  return card
}

describe('ordered rules editor', () => {
  it('shows authoritative counts and conservative shadow warnings, then reorders by revision', async () => {
    const reordered = { ...orderedRules, revision: 'rules-b', etag: '"rules-b"', rules: [...orderedRules.rules].reverse() }
    const client = stubClient({
      getRules: vi.fn().mockResolvedValue(orderedRules),
      getFlows: vi.fn().mockResolvedValue(flowPage),
      reorderRules: vi.fn().mockResolvedValue(reordered),
    })
    const announce = vi.fn()
    const user = userEvent.setup()
    render(<RulesEditor client={client} announce={announce} />)

    expect(await screen.findByText('Potentially shadowed by')).toBeInTheDocument()
    const webCard = cardFor('web-traffic')
    expect(within(webCard).getByText('1 currently controlled')).toBeInTheDocument()

    await user.click(within(webCard).getByRole('button', { name: 'Move web-traffic up' }))

    await waitFor(() => expect(client.reorderRules).toHaveBeenCalledWith(['web-traffic', 'all-tcp'], '"rules-a"'))
    expect(announce).toHaveBeenCalledWith('Rule web-traffic moved up.', 'success')
  })

  it('edits one rule in place without changing its ID', async () => {
    const client = stubClient({ getRules: vi.fn().mockResolvedValue(orderedRules) })
    const user = userEvent.setup()
    render(<RulesEditor client={client} announce={vi.fn()} />)
    await screen.findByText('web-traffic')
    const webCard = cardFor('web-traffic')

    await user.click(within(webCard).getByRole('button', { name: 'Edit' }))
    const dialog = await screen.findByRole('dialog', { name: /define match and outcome/i })
    const name = within(dialog).getByRole('textbox', { name: 'Display name' })
    await user.clear(name)
    await user.type(name, 'Secure web')
    await user.selectOptions(within(dialog).getByLabelText('Action'), 'ignore')
    await user.click(within(dialog).getByRole('button', { name: 'Save rule' }))

    await waitFor(() => expect(client.replaceRule).toHaveBeenCalledWith(
      'web-traffic',
      expect.objectContaining({ id: 'web-traffic', name: 'Secure web', action: { state: 'ignore', channel: 4 } }),
      '"rules-a"',
    ))
  })

  it('imports the complete ordered collection atomically', async () => {
    const client = stubClient({ getRules: vi.fn().mockResolvedValue(orderedRules) })
    const user = userEvent.setup()
    render(<RulesEditor client={client} announce={vi.fn()} />)
    await screen.findByText('web-traffic')

    await user.click(screen.getByRole('button', { name: 'Import / export' }))
    const dialog = await screen.findByRole('dialog', { name: /import or export the full order/i })
    const imported = [orderedRules.rules[1]!]
    const textarea = within(dialog).getByRole('textbox', { name: 'Rules JSON' })
    fireEvent.change(textarea, { target: { value: JSON.stringify({ rules: imported }) } })
    await user.click(within(dialog).getByRole('button', { name: 'Import atomically' }))

    await waitFor(() => expect(client.replaceRules).toHaveBeenCalledWith(imported, '"rules-a"'))
  })
})
