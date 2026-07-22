import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App'
import { snapshot, stubClient } from './test/fixtures'

afterEach(() => {
	window.history.replaceState(null, '', '/')
	vi.unstubAllGlobals()
})

describe('setup assistant', () => {
	it('prepares a complete edge role and peer destination for restart', async () => {
		const client = stubClient({
			validateConfig: vi.fn().mockResolvedValue({
				revision: 'public-revision',
				hot_fields: [],
				restart_required_fields: ['instance.role', 'midi.enabled', 'peer.enabled', 'peer.url', 'peer.token'],
			}),
		})
		const user = userEvent.setup()
		render(<App client={client} />)

		await screen.findByRole('heading', { name: /choose this node's job/i })
		await user.selectOptions(screen.getByLabelText(/runtime role/i), 'edge')
		await user.type(screen.getByLabelText(/host websocket url/i), 'wss://music.example/api/v1/peer')
		await user.type(screen.getByLabelText(/peer bearer token/i), 'sixteen-byte-token')
		await user.click(screen.getByRole('button', { name: /03midi/i }))
		expect(screen.getByRole('checkbox', { name: /midi output/i })).toBeDisabled()
		expect(screen.getByRole('checkbox', { name: /midi output/i })).not.toBeChecked()
		await user.click(screen.getByRole('button', { name: /review/i }))
		await user.click(screen.getByRole('button', { name: /^validate$/i }))
		await user.click(await screen.findByRole('button', { name: /save for restart/i }))

		await waitFor(() => expect(client.stageConfig).toHaveBeenCalledWith(
			expect.objectContaining({
				instance: expect.objectContaining({ role: 'edge' }),
				midi: expect.objectContaining({ enabled: false }),
				peer: expect.objectContaining({ enabled: true, url: 'wss://music.example/api/v1/peer', token: 'sixteen-byte-token' }),
			}),
			'"public-revision"',
		))
	})

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

  it('saves restart-required changes without applying them to the active runtime', async () => {
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
		await user.click(screen.getByRole('button', { name: /capture/i }))
    await user.selectOptions(screen.getByRole('combobox', { name: /^capture interface/i }), 'lo0')
    await user.click(screen.getByRole('button', { name: /review/i }))
    await user.click(screen.getByRole('button', { name: /^validate$/i }))

    expect(await screen.findByText('Restart required')).toBeInTheDocument()
    expect(screen.getByText('capture.interface')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /save for restart/i }))

    await waitFor(() => expect(client.stageConfig).toHaveBeenCalledWith(
      expect.objectContaining({ capture: expect.objectContaining({ interface: 'lo0' }) }),
      '"public-revision"',
    ))
    expect(client.updateConfig).not.toHaveBeenCalled()
  })

  it('loads and can discard a configuration already saved for restart', async () => {
    const pending = {
      config: { ...structuredClone(snapshot.config.config), capture: { ...snapshot.config.config.capture, interface: 'lo0' } },
      revision: '"pending-revision"',
    }
    const client = stubClient({
      getStatus: vi.fn().mockResolvedValue({
        ...snapshot.status,
        state: 'restart_pending',
        pending_revision: 'pending-revision',
        warning: 'configuration is saved and will take effect after restart',
      }),
      getPendingConfig: vi.fn().mockResolvedValue(pending),
    })
    const user = userEvent.setup()
    render(<App client={client} />)

    await user.click(await screen.findByRole('button', { name: /review/i }))
    expect(await screen.findByText('Saved for restart')).toBeInTheDocument()
    expect(screen.getByText('lo0')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /discard pending/i }))
    await waitFor(() => expect(client.cancelPendingConfig).toHaveBeenCalledWith('"pending-revision"'))
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

	it('navigates to the ordered rules workspace', async () => {
    const client = stubClient()
    const user = userEvent.setup()
    render(<App client={client} />)

    await user.click(await screen.findByRole('link', { name: 'Rules' }))

    expect(await screen.findByRole('heading', { name: /order the traffic/i })).toBeInTheDocument()
		expect(window.location.pathname).toBe('/rules')
	})

	it('navigates to the live musical viewer', async () => {
		class TestWebSocket {
			onopen = null
			onmessage = null
			onclose = null
			onerror = null
			close() {}
		}
		vi.stubGlobal('WebSocket', TestWebSocket)
		const client = stubClient()
		const user = userEvent.setup()
		render(<App client={client} />)

		await user.click(await screen.findByRole('link', { name: 'Viewer' }))

		expect(await screen.findByRole('heading', { name: /see what the scheduler accepted/i })).toBeInTheDocument()
		expect(window.location.pathname).toBe('/viewer')
	})

	it('navigates to role-aware peer status', async () => {
		const client = stubClient()
		const user = userEvent.setup()
		render(<App client={client} />)

		await user.click(await screen.findByRole('link', { name: 'Peers' }))

		expect(await screen.findByRole('heading', { name: /peer transport at a glance/i })).toBeInTheDocument()
		expect(window.location.pathname).toBe('/peers')
	})
})
