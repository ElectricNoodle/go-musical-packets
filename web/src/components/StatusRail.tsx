import type { RuntimeSnapshot } from '../api/types'

interface StatusRailProps {
  snapshot: RuntimeSnapshot
  onPanic: () => Promise<void>
  busy: boolean
}

export function StatusRail({ snapshot, onPanic, busy }: StatusRailProps) {
  const midiLabel = !snapshot.midi.enabled
    ? 'Disabled'
    : snapshot.midi.connected
      ? snapshot.midi.current?.name ?? 'Connected'
      : 'Waiting for device'

  return (
    <aside className="status-rail" aria-label="Runtime status">
      <div className="status-block">
        <span className="eyebrow">Runtime</span>
        <strong className={`signal signal--${snapshot.status.state === 'ready' ? 'good' : 'warn'}`}>
          {snapshot.status.state}
        </strong>
        <small>{snapshot.status.state === 'restart_pending' ? 'Restart configuration saved' : snapshot.status.writable ? 'Transactional writes enabled' : 'Read-only configuration'}</small>
      </div>

      <div className="status-block">
        <span className="eyebrow">Capture</span>
        <strong>{snapshot.interfaces.selected || 'No interface selected'}</strong>
        <small>{snapshot.config.config.capture.enabled ? 'Capture enabled' : 'Capture paused by configuration'}</small>
      </div>

      <div className="status-block">
        <span className="eyebrow">MIDI</span>
        <strong>{midiLabel}</strong>
        <small>{snapshot.midi.discovery === 'error' ? 'Discovery needs attention' : 'Discovery available'}</small>
      </div>

      <button className="panic-button" type="button" onClick={() => void onPanic()} disabled={busy || !snapshot.midi.enabled}>
        <span aria-hidden="true">■</span>
        All notes off
      </button>
    </aside>
  )
}
