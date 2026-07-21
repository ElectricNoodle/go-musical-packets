import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/client'
import { flowPage, rulesDocument, stubClient } from '../test/fixtures'
import { RuleCreator } from './RuleCreator'

function firstFlow() {
  const flow = flowPage.flows.at(0)
  if (!flow) throw new Error('flow fixture is empty')
  return flow
}

describe('rule creator', () => {
  it('pins one exact flow with the authoritative rules revision', async () => {
    const client = stubClient()
    const onCreated = vi.fn().mockResolvedValue(undefined)
    const onClose = vi.fn()
    const user = userEvent.setup()
    render(<RuleCreator client={client} flow={firstFlow()} announce={vi.fn()} onCreated={onCreated} onClose={onClose} />)

    await user.clear(await screen.findByLabelText(/rule id/i))
    await user.type(screen.getByLabelText(/rule id/i), 'pinned-web')
    await user.click(screen.getByRole('button', { name: /pin flow/i }))

    await waitFor(() => expect(client.createRule).toHaveBeenCalledWith(expect.objectContaining({
      id: 'pinned-web',
      enabled: true,
      match: { exact_flow_id: '0123456789abcdef01234567' },
      action: { state: 'play', channel: 4 },
    }), '"rules-revision-a"'))
    expect(onCreated).toHaveBeenCalledOnce()
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('generalizes the latest directional destination into a service rule', async () => {
    const client = stubClient()
    const user = userEvent.setup()
    render(<RuleCreator client={client} flow={firstFlow()} announce={vi.fn()} onCreated={vi.fn()} onClose={vi.fn()} />)

    await user.selectOptions(await screen.findByLabelText(/match scope/i), 'destination_service')
    await user.click(screen.getByRole('button', { name: /create rule/i }))

    await waitFor(() => expect(client.createRule).toHaveBeenCalledWith(expect.objectContaining({
      match: {
        protocol: 'tcp',
        destination_cidr: '198.51.100.20/32',
        destination_ports: { minimum: 443, maximum: 443 },
      },
    }), '"rules-revision-a"'))
  })

  it('reloads a conflicting rules revision without retrying the write', async () => {
    const client = stubClient({
      getRules: vi.fn().mockResolvedValue(rulesDocument),
      createRule: vi.fn().mockRejectedValue(new ApiError({
        status: 412, code: 'precondition_failed', detail: 'the rule revision is stale',
      })),
    })
    const user = userEvent.setup()
    render(<RuleCreator client={client} flow={firstFlow()} announce={vi.fn()} onCreated={vi.fn()} onClose={vi.fn()} />)

    await user.click(await screen.findByRole('button', { name: /pin flow/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/rules changed in another session/i)
    expect(client.getRules).toHaveBeenCalledTimes(2)
    expect(client.createRule).toHaveBeenCalledOnce()
  })
})
