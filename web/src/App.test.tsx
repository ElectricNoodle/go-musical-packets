import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App'
import { stubClient } from './test/fixtures'

afterEach(() => window.history.replaceState(null, '', '/'))

describe('setup assistant', () => {
  it('loads runtime boundaries and applies a validated live-safe change', async () => {
    const client = stubClient({
      validateConfig: vi.fn().mockResolvedValue({
        revision: 'public-revision',
        hot_fields: ['mapping.default_state'],
        restart_required_fields: [],
      }),
    })
    const user = userEvent.setup()
    render(<App client={client} />)

    expect(await screen.findByRole('heading', { name: /shape traffic/i })).toBeInTheDocument()
    expect(screen.getByText('USB Synth')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /safety/i }))
    await user.selectOptions(screen.getByLabelText(/unmatched traffic/i), 'play')
    await user.click(screen.getByRole('button', { name: /review/i }))
    await user.click(screen.getByRole('button', { name: /^validate$/i }))

    expect(await screen.findByText('Ready to apply')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /apply live-safe changes/i }))

    await waitFor(() => expect(client.updateConfig).toHaveBeenCalled())
    expect(client.updateConfig).toHaveBeenCalledWith(
      expect.objectContaining({ mapping: expect.objectContaining({ default_state: 'play' }) }),
      '"public-revision"',
    )
  })

  it('identifies restart-required changes without offering a false apply path', async () => {
    const client = stubClient({
      validateConfig: vi.fn().mockResolvedValue({
        revision: 'public-revision',
        hot_fields: [],
        restart_required_fields: ['capture.interface'],
      }),
    })
    const user = userEvent.setup()
    render(<App client={client} />)

    await screen.findByRole('heading', { name: /shape traffic/i })
    await user.selectOptions(screen.getByRole('combobox', { name: /^capture interface/i }), 'lo0')
    await user.click(screen.getByRole('button', { name: /review/i }))
    await user.click(screen.getByRole('button', { name: /^validate$/i }))

    expect(await screen.findByText('Restart required')).toBeInTheDocument()
    expect(screen.getByText('capture.interface')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /apply live-safe changes/i })).toBeDisabled()
  })

  it('routes MIDI panic through the management client', async () => {
    const client = stubClient()
    const user = userEvent.setup()
    render(<App client={client} />)

    await user.click(await screen.findByRole('button', { name: /all notes off/i }))
    await waitFor(() => expect(client.panicMIDI).toHaveBeenCalledOnce())
    expect(screen.getByRole('status')).toHaveTextContent(/all notes off sent/i)
  })

  it('navigates to the flow explorer without reloading the application shell', async () => {
    const client = stubClient()
    const user = userEvent.setup()
    render(<App client={client} />)

    await user.click(await screen.findByRole('link', { name: 'Flows' }))

    expect(await screen.findByRole('heading', { name: /find the traffic worth hearing/i })).toBeInTheDocument()
    expect(window.location.pathname).toBe('/flows')
  })
})
