import { useMemo, useState } from 'react'
import { ApiError, type ManagementClient } from '../api/client'
import type { Configuration, FlowState, RuntimeSnapshot, Validation } from '../api/types'

const steps = ['Capture', 'MIDI', 'Safety', 'Review'] as const
type Step = (typeof steps)[number]

interface SetupAssistantProps {
  client: ManagementClient
  snapshot: RuntimeSnapshot
  onApplied: () => Promise<void>
  announce: (message: string, tone?: 'neutral' | 'success' | 'error') => void
}

function cloneConfiguration(configuration: Configuration): Configuration {
  return structuredClone(configuration)
}

function errorMessage(error: unknown): string {
  if (error instanceof ApiError || error instanceof Error) {
    return error.message
  }
  return 'The management operation failed.'
}

export function SetupAssistant({ client, snapshot, onApplied, announce }: SetupAssistantProps) {
  const baseline = snapshot.pending?.config ?? snapshot.config.config
  const [stepIndex, setStepIndex] = useState(0)
  const [draft, setDraft] = useState(() => cloneConfiguration(baseline))
  const [validation, setValidation] = useState<Validation | null>(null)
  const [busy, setBusy] = useState<'validate' | 'apply' | 'stage' | 'cancel' | 'audition' | null>(null)
  const step = steps[stepIndex] as Step
  const dirty = useMemo(() => JSON.stringify(draft) !== JSON.stringify(baseline), [baseline, draft])

  const change = (mutate: (next: Configuration) => void) => {
    setDraft((current) => {
      const next = cloneConfiguration(current)
      mutate(next)
      return next
    })
    setValidation(null)
  }

  const validate = async () => {
    setBusy('validate')
    try {
      const result = await client.validateConfig(draft)
      setValidation(result)
      announce(
        result.restart_required_fields.length > 0
          ? 'Configuration is valid. Some changes require a process restart.'
          : 'Configuration is valid and can be applied safely.',
        'success',
      )
    } catch (error) {
      setValidation(null)
      announce(errorMessage(error), 'error')
    } finally {
      setBusy(null)
    }
  }

  const apply = async () => {
    if (!validation || validation.restart_required_fields.length > 0 || snapshot.pending) return
    setBusy('apply')
    try {
      await client.updateConfig(draft, snapshot.config.revision)
      announce('Configuration applied atomically.', 'success')
      await onApplied()
    } catch (error) {
      announce(errorMessage(error), 'error')
    } finally {
      setBusy(null)
    }
  }

  const stage = async () => {
    if (!validation || validation.restart_required_fields.length === 0) return
    setBusy('stage')
    try {
      await client.stageConfig(draft, snapshot.pending?.revision ?? snapshot.config.revision)
      announce('Configuration saved for the next process restart.', 'success')
      await onApplied()
    } catch (error) {
      announce(errorMessage(error), 'error')
    } finally {
      setBusy(null)
    }
  }

  const cancelPending = async () => {
    if (!snapshot.pending) return
    setBusy('cancel')
    try {
      await client.cancelPendingConfig(snapshot.pending.revision)
      announce('Pending restart configuration discarded.', 'success')
      await onApplied()
    } catch (error) {
      announce(errorMessage(error), 'error')
    } finally {
      setBusy(null)
    }
  }

  const audition = async () => {
    setBusy('audition')
    try {
      await client.auditionMIDI(draft.mapping.default_channel)
      announce(`Played C4 on channel ${draft.mapping.default_channel}.`, 'success')
    } catch (error) {
      announce(errorMessage(error), 'error')
    } finally {
      setBusy(null)
    }
  }

  return (
    <section className="assistant" aria-labelledby="setup-title">
      <header className="assistant__header">
        <div>
          <span className="eyebrow">Stage 11 · setup assistant</span>
          <h1 id="setup-title">Shape traffic into an instrument.</h1>
          <p>Configure the signal path in four deliberate passes. Nothing is applied until validation succeeds.</p>
        </div>
        <div className="revision" title={snapshot.pending?.revision ?? snapshot.status.revision}>
          <span>{snapshot.pending ? 'next start' : 'revision'}</span>
          <code>{(snapshot.pending?.revision ?? snapshot.status.revision).slice(0, 8)}</code>
        </div>
      </header>

      <nav className="stepper" aria-label="Setup progress">
        {steps.map((item, index) => (
          <button
            key={item}
            type="button"
            className={index === stepIndex ? 'step step--active' : index < stepIndex ? 'step step--complete' : 'step'}
            aria-current={index === stepIndex ? 'step' : undefined}
            onClick={() => setStepIndex(index)}
          >
            <span>{String(index + 1).padStart(2, '0')}</span>
            {item}
          </button>
        ))}
      </nav>

      <div className="assistant__body">
        {step === 'Capture' && (
          <div className="step-panel">
            <div className="section-heading">
              <span className="section-index">01</span>
              <div><h2>Choose the signal</h2><p>Capture stays silent by default; observed flows become visible before they become musical.</p></div>
            </div>
            <div className="field-grid">
              <label className="toggle-card">
                <span><strong>Packet capture</strong><small>Open the selected interface when the process starts.</small></span>
                <input type="checkbox" checked={draft.capture.enabled} onChange={(event) => change((next) => { next.capture.enabled = event.target.checked })} />
                <i aria-hidden="true" />
              </label>
              <label className="field">
                <span>Capture interface</span>
                <select value={draft.capture.interface} onChange={(event) => change((next) => { next.capture.interface = event.target.value })}>
                  <option value="auto">Automatic · {snapshot.interfaces.selected || 'none available'}</option>
                  {draft.capture.interface !== 'auto' && !snapshot.interfaces.interfaces.some((candidate) => candidate.name === draft.capture.interface) && (
                    <option value={draft.capture.interface}>{draft.capture.interface} · currently unavailable</option>
                  )}
                  {snapshot.interfaces.interfaces.map((candidate) => (
                    <option key={candidate.name} value={candidate.name}>{candidate.name}{candidate.description ? ` · ${candidate.description}` : ''}</option>
                  ))}
                </select>
                <small>Changing interfaces is validated now and takes effect after restart.</small>
              </label>
              <label className="field field--wide">
                <span>Broad capture filter</span>
                <input value={draft.capture.bpf} onChange={(event) => change((next) => { next.capture.bpf = event.target.value })} placeholder="ip or ip6" spellCheck={false} />
                <small>Use a libpcap BPF expression. Application HTTP traffic is excluded automatically.</small>
              </label>
            </div>
            <div className="device-list" aria-label="Discovered capture interfaces">
              {snapshot.interfaces.interfaces.map((candidate) => (
                <article key={candidate.name} className={candidate.name === snapshot.interfaces.selected ? 'device device--selected' : 'device'}>
                  <div><strong>{candidate.name}</strong><span>{candidate.up ? 'up' : 'down'}{candidate.loopback ? ' · loopback' : ''}</span></div>
                  <small>{candidate.addresses.join(' · ') || 'No addresses reported'}</small>
                </article>
              ))}
            </div>
          </div>
        )}

        {step === 'MIDI' && (
          <div className="step-panel">
            <div className="section-heading">
              <span className="section-index">02</span>
              <div><h2>Choose the voice</h2><p>The runtime reconnects automatically and always owns Note Off scheduling.</p></div>
            </div>
            <div className="field-grid">
              <label className="toggle-card">
                <span><strong>MIDI output</strong><small>Send accepted notes to a local physical or virtual port.</small></span>
                <input type="checkbox" checked={draft.midi.enabled} onChange={(event) => change((next) => { next.midi.enabled = event.target.checked })} />
                <i aria-hidden="true" />
              </label>
              <label className="field">
                <span>Preferred device</span>
                <select value={draft.midi.exact_device_name} onChange={(event) => change((next) => { next.midi.exact_device_name = event.target.value })}>
                  <option value="">First usable output</option>
                  {draft.midi.exact_device_name && !snapshot.midi.devices.some((device) => device.name === draft.midi.exact_device_name) && (
                    <option value={draft.midi.exact_device_name}>{draft.midi.exact_device_name} · currently unavailable</option>
                  )}
                  {snapshot.midi.devices.map((device) => <option key={`${device.number}-${device.name}`} value={device.name}>{device.name}</option>)}
                </select>
                <small>Exact name takes precedence over the configured regular expression.</small>
              </label>
              <label className="field">
                <span>Default MIDI channel</span>
                <input type="number" min="1" max="16" value={draft.mapping.default_channel} onChange={(event) => change((next) => { next.mapping.default_channel = Number(event.target.value) })} />
                <small>User-facing channels stay in the range 1 through 16.</small>
              </label>
              <div className="audition-card">
                <div className={snapshot.midi.connected ? 'port-light port-light--on' : 'port-light'} aria-hidden="true" />
                <div><strong>{snapshot.midi.current?.name ?? 'No output connected'}</strong><small>{snapshot.midi.discovery === 'error' ? 'Last discovery failed' : 'Cached device discovery is healthy'}</small></div>
                <button type="button" className="secondary-button" onClick={() => void audition()} disabled={busy !== null || !snapshot.midi.enabled}>Audition C4</button>
              </div>
            </div>
          </div>
        )}

        {step === 'Safety' && (
          <div className="step-panel">
            <div className="section-heading">
              <span className="section-index">03</span>
              <div><h2>Set the guardrails</h2><p>Bounded queues and musical limits keep traffic bursts expressive rather than destructive.</p></div>
            </div>
            <div className="field-grid field-grid--three">
              <label className="field">
                <span>Unmatched traffic</span>
                <select value={draft.mapping.default_state} onChange={(event) => change((next) => { next.mapping.default_state = event.target.value as FlowState })}>
                  <option value="monitor">Monitor · recommended</option>
                  <option value="ignore">Ignore</option>
                  <option value="play">Play</option>
                </select>
                <small>Monitor discovers flows without producing notes.</small>
              </label>
              <label className="field">
                <span>Maximum notes / second</span>
                <input type="number" min="1" value={draft.performance.maximum_notes_per_second} onChange={(event) => change((next) => { next.performance.maximum_notes_per_second = Number(event.target.value) })} />
                <small>Global scheduler rate ceiling.</small>
              </label>
              <label className="field">
                <span>Maximum polyphony</span>
                <input type="number" min="1" max="128" value={draft.performance.maximum_polyphony} onChange={(event) => change((next) => { next.performance.maximum_polyphony = Number(event.target.value) })} />
                <small>Simultaneous channel and pitch pairs.</small>
              </label>
            </div>
            <div className="safety-strip">
              <span><b>{draft.performance.packet_queue_capacity.toLocaleString()}</b> packet queue</span>
              <span><b>{draft.performance.note_queue_capacity.toLocaleString()}</b> note queue</span>
              <span><b>{draft.performance.flow_registry_capacity.toLocaleString()}</b> retained flows</span>
              <span><b>{draft.performance.flow_ttl}</b> flow lifetime</span>
            </div>
          </div>
        )}

        {step === 'Review' && (
          <div className="step-panel">
            <div className="section-heading">
              <span className="section-index">04</span>
              <div><h2>Validate before applying</h2><p>The Go backend remains authoritative. Validation uses the same strict rules as startup.</p></div>
            </div>
            <div className="review-grid">
              <dl className="summary-card">
                <div><dt>Instance</dt><dd>{draft.instance.id} · {draft.instance.role}</dd></div>
                <div><dt>Capture</dt><dd>{draft.capture.enabled ? draft.capture.interface : 'disabled'}</dd></div>
                <div><dt>Default action</dt><dd>{draft.mapping.default_state} · channel {draft.mapping.default_channel}</dd></div>
                <div><dt>MIDI</dt><dd>{draft.midi.enabled ? draft.midi.exact_device_name || 'first usable output' : 'disabled'}</dd></div>
                <div><dt>Safety</dt><dd>{draft.performance.maximum_notes_per_second} notes/s · {draft.performance.maximum_polyphony} voices</dd></div>
              </dl>
              <div className="validation-card" aria-live="polite">
                {!validation && snapshot.pending && !dirty && <><span className="validation-icon validation-icon--warn">↻</span><strong>Saved for restart</strong><p>The active runtime is unchanged. This complete configuration will load on the next process start.</p></>}
                {!validation && (!snapshot.pending || dirty) && <><span className="validation-icon">?</span><strong>Not validated</strong><p>Run validation to classify live-safe and restart-required changes.</p></>}
                {validation && validation.restart_required_fields.length === 0 && !snapshot.pending && <><span className="validation-icon validation-icon--good">✓</span><strong>Ready to apply</strong><p>{validation.hot_fields.length ? `${validation.hot_fields.length} live field change${validation.hot_fields.length === 1 ? '' : 's'} will be applied atomically.` : 'The active configuration already matches this draft.'}</p></>}
                {validation && validation.restart_required_fields.length === 0 && snapshot.pending && <><span className="validation-icon validation-icon--warn">→</span><strong>Discard pending first</strong><p>This draft no longer requires restart. Discard the saved generation before applying live-safe changes.</p></>}
                {validation && validation.restart_required_fields.length > 0 && <><span className="validation-icon validation-icon--warn">↻</span><strong>Restart required</strong><p>Save this complete configuration now and it will become active on the next process start.</p><ul>{validation.restart_required_fields.map((field) => <li key={field}><code>{field}</code></li>)}</ul></>}
              </div>
            </div>
            {!snapshot.status.writable && <p className="inline-notice">This process was started without a writable configuration repository. You can inspect and validate, but not apply changes.</p>}
            <div className="review-actions">
              <button type="button" className="secondary-button" onClick={() => { setDraft(cloneConfiguration(baseline)); setValidation(null) }} disabled={!dirty || busy !== null}>Reset draft</button>
              {snapshot.pending && <button type="button" className="secondary-button" onClick={() => void cancelPending()} disabled={busy !== null}>{busy === 'cancel' ? 'Discarding…' : 'Discard pending'}</button>}
              <button type="button" className="secondary-button" onClick={() => void validate()} disabled={busy !== null}>{busy === 'validate' ? 'Validating…' : 'Validate'}</button>
              {validation && validation.restart_required_fields.length > 0
                ? <button type="button" className="primary-button" onClick={() => void stage()} disabled={!dirty || !snapshot.status.writable || busy !== null}>{busy === 'stage' ? 'Saving…' : snapshot.pending ? 'Update next restart' : 'Save for restart'}</button>
                : <button type="button" className="primary-button" onClick={() => void apply()} disabled={!dirty || Boolean(snapshot.pending) || !snapshot.status.writable || busy !== null || !validation}>{busy === 'apply' ? 'Applying…' : 'Apply live-safe changes'}</button>}
            </div>
          </div>
        )}
      </div>

      <footer className="assistant__footer">
        <button type="button" className="text-button" onClick={() => setStepIndex((current) => Math.max(0, current - 1))} disabled={stepIndex === 0}>← Back</button>
        <span>{dirty ? 'Unsaved draft' : snapshot.pending ? 'Restart configuration saved' : 'Configuration unchanged'}</span>
        <button type="button" className="primary-button" onClick={() => setStepIndex((current) => Math.min(steps.length - 1, current + 1))} disabled={stepIndex === steps.length - 1}>Continue →</button>
      </footer>
    </section>
  )
}
