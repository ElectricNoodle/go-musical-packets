import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { MusicalViewer } from './MusicalViewer'
import type { LiveNoteBatch, LiveNoteEvent } from '../api/types'

class FakeSocket {
	onopen: ((event: Event) => void) | null = null
	onmessage: ((event: MessageEvent<unknown>) => void) | null = null
	onclose: ((event: CloseEvent) => void) | null = null
	onerror: ((event: Event) => void) | null = null
	closed = false
	close() { this.closed = true }
	emitOpen() { this.onopen?.(new Event('open')) }
	emit(value: unknown) { this.onmessage?.(new MessageEvent('message', { data: JSON.stringify(value) })) }
}

function note(id: string, midiNote = 60): LiveNoteEvent {
	const accepted = new Date().toISOString()
	return {
		id, origin: 'test-node', sequence: 1, mapping_version: 'flow-mode-v1', flow_id: `flow-${id}`,
		mode: 'dorian', root: 2, note: midiNote, velocity: 96, duration_ms: 10_000, channel: 3,
		created_at: accepted, accepted_at: accepted,
	}
}

function batch(notes: LiveNoteEvent[], overrides: Partial<LiveNoteBatch> = {}): LiveNoteBatch {
	return {
		type: 'notes', sent_at: new Date().toISOString(), dropped: 0,
		packet_total: 10, note_total: notes.length, notes, ...overrides,
	}
}

describe('musical viewer', () => {
	it('renders accepted notes and ignores malformed stream messages', async () => {
		const socket = new FakeSocket()
		render(<MusicalViewer connect={() => socket} />)

		act(() => {
			socket.emitOpen()
			socket.emit(null)
			socket.emit(batch([null as unknown as LiveNoteEvent]))
			socket.emit(batch([note('accepted')]))
		})

		expect(screen.getByText('live')).toBeInTheDocument()
		const mapping = screen.getByText('Selected mapping').closest('section')
		expect(mapping).not.toBeNull()
		expect(within(mapping!).getByText('C4')).toBeInTheDocument()
		expect(within(mapping!).getByText(/D dorian · channel 3/i)).toBeInTheDocument()
		expect(screen.getByText('1/512 retained · 0 dropped')).toBeInTheDocument()
	})

	it('bounds history and lets the operator pause visual ingestion', async () => {
		const socket = new FakeSocket()
		const user = userEvent.setup()
		render(<MusicalViewer connect={() => socket} />)
		const notes = Array.from({ length: 513 }, (_, index) => note(String(index), 48 + index % 24))

		act(() => socket.emit(batch(notes, { note_total: 513 })))
		expect(screen.getByText('512/512 retained · 0 dropped')).toBeInTheDocument()

		await user.click(screen.getByRole('button', { name: 'Pause view' }))
		act(() => socket.emit(batch([note('paused')], { note_total: 514 })))
		expect(screen.getByText('512/512 retained · 0 dropped')).toBeInTheDocument()
		expect(screen.getByRole('button', { name: 'Resume' })).toHaveAttribute('aria-pressed', 'true')
	})

	it('derives packet and accepted-note rates from cumulative counters', () => {
		const socket = new FakeSocket()
		render(<MusicalViewer connect={() => socket} />)
		const start = Date.now()

		act(() => {
			socket.emit(batch([], { sent_at: new Date(start).toISOString(), packet_total: 100, note_total: 10 }))
			socket.emit(batch([], { sent_at: new Date(start + 1000).toISOString(), packet_total: 120, note_total: 12 }))
		})

		expect(screen.getByText('20.0/s')).toBeInTheDocument()
		expect(screen.getByText('2.0/s')).toBeInTheDocument()
	})
})
