import { useEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
import * as echarts from 'echarts/core'
import { CustomChart, LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { LiveNoteBatch, LiveNoteEvent } from '../api/types'

echarts.use([CustomChart, LineChart, GridComponent, TooltipComponent, CanvasRenderer])

const HISTORY_CAPACITY = 512
const RATE_CAPACITY = 60
const WINDOW_MS = 20_000
const pitchNames = ['C', 'C♯', 'D', 'D♯', 'E', 'F', 'F♯', 'G', 'G♯', 'A', 'A♯', 'B']
const channelColors = ['#c9ff57', '#69d9d0', '#ffba5a', '#ff796c', '#9e8cff', '#f074c4', '#70b8ff', '#d8dc63', '#80e09b', '#e9975a', '#68d5ed', '#dc86ff', '#ffcb7a', '#82a9ff', '#e76f91', '#a8e06c']

type StreamState = 'connecting' | 'live' | 'reconnecting'
type ColorMode = 'channel' | 'mode' | 'source' | 'flow'

const modeIntervals: Record<string, number[]> = {
	ionian: [0, 2, 4, 5, 7, 9, 11], dorian: [0, 2, 3, 5, 7, 9, 10],
	phrygian: [0, 1, 3, 5, 7, 8, 10], lydian: [0, 2, 4, 6, 7, 9, 11],
	mixolydian: [0, 2, 4, 5, 7, 9, 10], aeolian: [0, 2, 3, 5, 7, 8, 10],
	locrian: [0, 1, 3, 5, 6, 8, 10],
}

interface RateSample {
	time: number
	packets: number
	notes: number
}

interface ViewerSocket {
	onopen: ((event: Event) => void) | null
	onmessage: ((event: MessageEvent<unknown>) => void) | null
	onclose: ((event: CloseEvent) => void) | null
	onerror: ((event: Event) => void) | null
	close(): void
}

interface MusicalViewerProps {
	connect?: () => ViewerSocket
}

function streamURL() {
	const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
	return `${protocol}//${window.location.host}/api/v1/events`
}

function defaultConnect(): ViewerSocket {
	return new WebSocket(streamURL())
}

function isBatch(value: unknown): value is LiveNoteBatch {
	if (!value || typeof value !== 'object') return false
	const batch = value as Partial<LiveNoteBatch>
	return batch.type === 'notes' && Array.isArray(batch.notes) && batch.notes.every(isLiveNote) &&
		Number.isFinite(batch.packet_total) && Number.isFinite(batch.note_total) && Number.isFinite(batch.dropped) &&
		(batch.packet_total ?? -1) >= 0 && (batch.note_total ?? -1) >= 0 && (batch.dropped ?? -1) >= 0 &&
		typeof batch.sent_at === 'string' && Number.isFinite(Date.parse(batch.sent_at))
}

function isLiveNote(value: unknown): value is LiveNoteEvent {
	if (!value || typeof value !== 'object') return false
	const note = value as Partial<LiveNoteEvent>
	return typeof note.id === 'string' && typeof note.origin === 'string' && typeof note.flow_id === 'string' &&
		typeof note.mapping_version === 'string' && typeof note.mode === 'string' &&
		Number.isInteger(note.root) && (note.root ?? -1) >= 0 && (note.root ?? 12) <= 11 &&
		Number.isInteger(note.note) && (note.note ?? -1) >= 0 && (note.note ?? 128) <= 127 &&
		Number.isInteger(note.velocity) && (note.velocity ?? 0) > 0 && (note.velocity ?? 128) <= 127 &&
		Number.isInteger(note.channel) && (note.channel ?? 0) >= 1 && (note.channel ?? 17) <= 16 &&
		Number.isFinite(note.duration_ms) && (note.duration_ms ?? 0) > 0 &&
		typeof note.accepted_at === 'string' && Number.isFinite(Date.parse(note.accepted_at)) &&
		typeof note.created_at === 'string'
}

function bounded<T>(previous: T[], incoming: T[], capacity: number) {
	const combined = [...previous, ...incoming]
	return combined.length > capacity ? combined.slice(combined.length - capacity) : combined
}

function pitchLabel(note: number) {
	return `${pitchNames[note % 12]}${Math.floor(note / 12) - 1}`
}

function stableColor(value: string) {
	let hash = 2166136261
	for (const character of value) hash = Math.imul(hash ^ character.charCodeAt(0), 16777619)
	return channelColors[Math.abs(hash) % channelColors.length] ?? channelColors[0]
}

function noteColor(note: LiveNoteEvent, mode: ColorMode) {
	if (mode === 'channel') return channelColors[note.channel - 1] ?? channelColors[0]
	return stableColor(mode === 'mode' ? note.mode : mode === 'source' ? note.origin : note.flow_id)
}

function PianoRoll({ notes, now, colorMode }: { notes: LiveNoteEvent[], now: number, colorMode: ColorMode }) {
	const container = useRef<HTMLDivElement>(null)

	useEffect(() => {
		if (!container.current || typeof ResizeObserver === 'undefined') return
		const chart = echarts.init(container.current, undefined, { renderer: 'canvas' })
		const resize = new ResizeObserver(() => chart.resize())
		resize.observe(container.current)
		return () => {
			resize.disconnect()
			chart.dispose()
		}
	}, [])

	useEffect(() => {
		if (!container.current) return
		const chart = echarts.getInstanceByDom(container.current)
		if (!chart) return
		const visible = notes.filter((note) => Date.parse(note.accepted_at) + note.duration_ms >= now - WINDOW_MS)
		chart.setOption({
			animation: false,
			backgroundColor: 'transparent',
			grid: { left: 48, right: 18, top: 18, bottom: 32 },
			xAxis: {
				type: 'time', min: now - WINDOW_MS, max: now + 1000,
				axisLine: { lineStyle: { color: '#434b3f' } },
				axisLabel: { color: '#788073', fontSize: 9 },
				splitLine: { show: true, lineStyle: { color: '#242924' } },
			},
			yAxis: {
				type: 'value', min: 23, max: 109, interval: 12,
				axisLabel: { color: '#788073', fontSize: 9, formatter: (value: number) => pitchLabel(value) },
				axisLine: { show: true, lineStyle: { color: '#434b3f' } },
				splitLine: { show: true, lineStyle: { color: '#242924' } },
			},
			tooltip: {
				trigger: 'item',
				formatter: (params: unknown) => {
					const data = (params as { data?: { value?: unknown[] } }).data?.value
						return data ? `${String(data[6])}<br/>${String(data[7])} · channel ${String(data[4])}<br/>velocity ${String(data[3])} · ${String(data[8])} ms` : ''
				},
			},
			series: [{
				type: 'custom',
				renderItem: (params: any, api: any) => {
					const start = api.coord([api.value(0), api.value(2)])
					const end = api.coord([api.value(1), api.value(2)])
					const height = Math.max(3, Math.abs(api.size([0, 1])[1]) * .68)
					const shape = echarts.graphic.clipRectByRect({ x: start[0], y: start[1] - height / 2, width: Math.max(2, end[0] - start[0]), height }, params.coordSys)
					return shape && { type: 'rect', shape, style: api.style({ fill: api.value(5), opacity: .28 + (api.value(3) / 127) * .72 }) }
				},
				encode: { x: [0, 1], y: 2 },
				data: visible.map((note) => ({
					value: [Date.parse(note.accepted_at), Date.parse(note.accepted_at) + note.duration_ms, note.note, note.velocity, note.channel, noteColor(note, colorMode), pitchLabel(note.note), note.mode, note.duration_ms],
				})),
			}, {
				type: 'line', symbol: 'none', silent: true,
				lineStyle: { color: '#f4f7ef', width: 1, opacity: .76 },
				data: [[now, 23], [now, 109]],
			}],
		}, true)
	}, [colorMode, notes, now])

	return <div className="piano-roll" ref={container} role="img" aria-label={`Live piano roll containing ${notes.length} accepted notes`} />
}

function RateStrip({ samples }: { samples: RateSample[] }) {
	const maximum = Math.max(1, ...samples.flatMap((sample) => [sample.packets, sample.notes]))
	const points = (field: 'packets' | 'notes') => samples.map((sample, index) => {
		const x = samples.length < 2 ? 100 : index / (samples.length - 1) * 100
		return `${x},${30 - sample[field] / maximum * 27}`
	}).join(' ')
	const latest = samples.at(-1)
	return (
		<div className="rate-strip" aria-label="Packet and accepted note rates">
			<div><span><i className="rate-key rate-key--packet" />Packets</span><strong>{latest?.packets.toFixed(1) ?? '0.0'}/s</strong></div>
			<div><span><i className="rate-key rate-key--note" />Accepted notes</span><strong>{latest?.notes.toFixed(1) ?? '0.0'}/s</strong></div>
			<svg viewBox="0 0 100 32" preserveAspectRatio="none" aria-hidden="true">
				<polyline className="rate-line rate-line--packet" points={points('packets')} />
				<polyline className="rate-line rate-line--note" points={points('notes')} />
			</svg>
		</div>
	)
}

export function MusicalViewer({ connect = defaultConnect }: MusicalViewerProps) {
	const [notes, setNotes] = useState<LiveNoteEvent[]>([])
	const [rates, setRates] = useState<RateSample[]>([])
	const [streamState, setStreamState] = useState<StreamState>('connecting')
	const [dropped, setDropped] = useState(0)
	const [paused, setPaused] = useState(false)
	const [colorMode, setColorMode] = useState<ColorMode>('channel')
	const [selectedAt, setSelectedAt] = useState<string | null>(null)
	const [now, setNow] = useState(Date.now())
	const pausedRef = useRef(paused)
	const previousTotals = useRef<{ time: number, packets: number, notes: number } | null>(null)

	useEffect(() => { pausedRef.current = paused }, [paused])
	useEffect(() => {
		const timer = window.setInterval(() => setNow(Date.now()), 100)
		return () => window.clearInterval(timer)
	}, [])

	useEffect(() => {
		let socket: ViewerSocket | null = null
		let reconnectTimer = 0
		let stopped = false
		let attempts = 0
		const open = () => {
			if (stopped) return
			setStreamState(attempts === 0 ? 'connecting' : 'reconnecting')
			socket = connect()
			socket.onopen = () => { attempts = 0; setStreamState('live') }
			socket.onmessage = (message) => {
				if (typeof message.data !== 'string') return
				let parsed: unknown
				try { parsed = JSON.parse(message.data) } catch { return }
				if (!isBatch(parsed)) return
				const timestamp = Date.parse(parsed.sent_at)
				const previous = previousTotals.current
				if (previous && Number.isFinite(timestamp) && timestamp > previous.time) {
					const seconds = (timestamp - previous.time) / 1000
					const sample = {
						time: timestamp,
						packets: Math.max(0, parsed.packet_total - previous.packets) / seconds,
						notes: Math.max(0, parsed.note_total - previous.notes) / seconds,
					}
					setRates((current) => bounded(current, [sample], RATE_CAPACITY))
				}
				previousTotals.current = { time: timestamp, packets: parsed.packet_total, notes: parsed.note_total }
				setDropped((current) => current + parsed.dropped)
				if (!pausedRef.current && parsed.notes.length > 0) {
					setNotes((current) => bounded(current, parsed.notes, HISTORY_CAPACITY))
				}
			}
			const reconnect = () => {
				if (stopped || reconnectTimer) return
				attempts++
				setStreamState('reconnecting')
				const delay = Math.min(10_000, 500 * 2 ** Math.min(attempts, 5))
				reconnectTimer = window.setTimeout(() => { reconnectTimer = 0; open() }, delay)
			}
			socket.onclose = reconnect
			socket.onerror = reconnect
		}
		open()
		return () => {
			stopped = true
			window.clearTimeout(reconnectTimer)
			socket?.close()
		}
	}, [connect])

	const active = useMemo(() => notes.filter((note) => Date.parse(note.accepted_at) <= now && Date.parse(note.accepted_at) + note.duration_ms > now), [notes, now])
	const latest = notes.at(-1)
	const selected = notes.find((note) => note.accepted_at === selectedAt) ?? latest
	const scale = selected ? (modeIntervals[selected.mode] ?? []).map((interval) => pitchNames[(selected.root + interval) % 12]) : []
	const channelCounts = useMemo(() => active.reduce((counts, note) => {
		counts[note.channel - 1] = (counts[note.channel - 1] ?? 0) + 1
		return counts
	}, Array.from({ length: 16 }, () => 0)), [active])
	const keyboard = Array.from({ length: 49 }, (_, index) => 36 + index)

	return (
		<section className="musical-viewer">
		<header className="viewer-header">
			<div>
				<span className="eyebrow">Live musical viewer</span>
				<h1>See what the scheduler accepted.</h1>
				<p>The playhead follows local playback time. Browser slowness can discard visual events, but can never delay capture or MIDI.</p>
			</div>
			<div className="viewer-controls">
				<span className={`stream-state stream-state--${streamState}`}><i />{streamState}</span>
				<label>Color by<select value={colorMode} onChange={(event) => setColorMode(event.target.value as ColorMode)}><option value="channel">Channel</option><option value="mode">Mode</option><option value="source">Source</option><option value="flow">Flow</option></select></label>
				<button className="secondary-button" type="button" aria-pressed={paused} onClick={() => setPaused((value) => !value)}>{paused ? 'Resume' : 'Pause view'}</button>
				<button className="text-button" type="button" onClick={() => { setNotes([]); setSelectedAt(null) }}>Clear</button>
			</div>
		</header>

		<div className="viewer-grid">
			<div className="viewer-main">
				<div className="panel-heading"><div><span>20 second window</span><strong>Piano roll</strong></div><small>{notes.length}/{HISTORY_CAPACITY} retained · {dropped} dropped</small></div>
				<PianoRoll notes={notes} now={now} colorMode={colorMode} />
				<div className="keyboard" aria-label="Illuminated MIDI keyboard">
					{keyboard.map((note) => {
						const sounding = active.find((event) => event.note === note)
						const sharp = [1, 3, 6, 8, 10].includes(note % 12)
						return <span key={note} className={`key key--${sharp ? 'black' : 'white'}${sounding ? ' key--active' : ''}`} style={sounding ? { '--key-color': noteColor(sounding, colorMode) } as CSSProperties : undefined}><i>{pitchLabel(note)}</i></span>
					})}
				</div>
			</div>

			<aside className="viewer-side">
				<section className="mapping-card">
					<span className="eyebrow">Selected mapping</span>
					{selected ? <><strong>{pitchLabel(selected.note)}</strong><p>{pitchNames[selected.root]} {selected.mode} · channel {selected.channel}</p><div className="scale-notes" aria-label={`${pitchNames[selected.root]} ${selected.mode} scale`}>{scale.map((pitch) => <span key={pitch}>{pitch}</span>)}</div><code>{selected.flow_id}</code><small>Velocity {selected.velocity} · {selected.duration_ms} ms · source {selected.origin}</small></> : <p>Waiting for an accepted note…</p>}
				</section>
				<section className="channel-lanes" aria-label="Active notes by MIDI channel">
					<div className="panel-heading"><div><span>Polyphony</span><strong>Channel lanes</strong></div><small>{active.length} active</small></div>
					{channelCounts.map((count, index) => <div className="channel-lane" key={index}><span>CH {String(index + 1).padStart(2, '0')}</span><i><b style={{ width: `${Math.min(100, count * 22)}%`, background: channelColors[index] }} /></i><strong>{count}</strong></div>)}
				</section>
			</aside>
		</div>

		<RateStrip samples={rates} />
		<section className="event-log" aria-label="Recent accepted note events">
			<div className="panel-heading"><div><span>Accessible history</span><strong>Event log</strong></div><small>Newest first</small></div>
			<div role="log" aria-live="polite">{notes.length === 0 ? <p>Accepted notes will appear here.</p> : notes.slice(-24).reverse().map((note) => <button type="button" aria-pressed={selected?.accepted_at === note.accepted_at} onClick={() => setSelectedAt(note.accepted_at)} key={`${note.id}:${note.accepted_at}`}><time>{new Date(note.accepted_at).toLocaleTimeString()}</time><strong>{pitchLabel(note.note)}</strong><span>Channel {note.channel}</span><span>{pitchNames[note.root]} {note.mode}</span><code>{note.flow_id}</code></button>)}</div>
		</section>
		</section>
	)
}
