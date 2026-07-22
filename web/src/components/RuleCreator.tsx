import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { ApiError, type ManagementClient } from '../api/client'
import type { FlowSnapshot, RuleConfig, RuleMatch, RulesDocument } from '../api/types'
import { MusicalIdentityFields, withActionState } from './RuleMusicalFields'

type RuleScope = 'exact' | 'protocol' | 'destination_service'

interface RuleCreatorProps {
  client: ManagementClient
  flow: FlowSnapshot
  onClose: () => void
  onCreated: () => void | Promise<void>
  announce: (message: string, tone?: 'neutral' | 'success' | 'error') => void
}

function endpoint(address: string, port: number) {
  const host = address.includes(':') ? `[${address}]` : address
  return port === 0 ? host : `${host}:${port}`
}

function hostPrefix(address: string) {
  return `${address}/${address.includes(':') ? 128 : 32}`
}

function matchFor(scope: RuleScope, flow: FlowSnapshot): RuleMatch {
  if (scope === 'exact') return { exact_flow_id: flow.id }
  if (scope === 'protocol') return { protocol: flow.protocol }
  const match: RuleMatch = {
    protocol: flow.protocol,
    destination_cidr: hostPrefix(flow.latest_destination.address),
  }
  if (flow.latest_destination.port > 0) {
    match.destination_ports = {
      minimum: flow.latest_destination.port,
      maximum: flow.latest_destination.port,
    }
  }
  return match
}

export function RuleCreator({ client, flow, onClose, onCreated, announce }: RuleCreatorProps) {
  const [document, setDocument] = useState<RulesDocument | null>(null)
  const [scope, setScope] = useState<RuleScope>('exact')
  const [id, setID] = useState(`flow-${flow.id.slice(0, 12)}`)
  const [name, setName] = useState(`Pinned ${flow.protocol.toUpperCase()} flow`)
  const [action, setAction] = useState<RuleConfig['action']>({ state: 'play', channel: flow.channel })
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    const controller = new AbortController()
    void client.getRules(controller.signal).then((next) => {
      setDocument(next)
      setError(null)
    }).catch((loadError) => {
      if (!controller.signal.aborted) setError(loadError instanceof Error ? loadError.message : 'Could not load persistent rules.')
    })
    return () => controller.abort()
  }, [client])

  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !busy) onClose()
    }
    window.addEventListener('keydown', closeOnEscape)
    return () => window.removeEventListener('keydown', closeOnEscape)
  }, [busy, onClose])

  const match = useMemo(() => matchFor(scope, flow), [flow, scope])
  const exactRule = document?.rules.find((rule) => rule.match.exact_flow_id === flow.id)
  const destination = endpoint(flow.latest_destination.address, flow.latest_destination.port)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!document) return
    const trimmedID = id.trim()
    if (!trimmedID) {
      setError('Rule ID is required.')
      return
    }
    if (scope === 'exact' && exactRule) {
      setError(`This flow is already pinned by ${exactRule.id}.`)
      return
    }

    const rule: RuleConfig = {
      id: trimmedID,
      name: name.trim(),
      enabled: true,
      match,
      action,
    }
    setBusy(true)
    setError(null)
    try {
      const next = await client.createRule(rule, document.etag)
      setDocument(next)
    } catch (createError) {
      if (createError instanceof ApiError && createError.status === 412) {
        try {
          const latest = await client.getRules()
          setDocument(latest)
          setError('Rules changed in another session. Review this rule and submit it again.')
        } catch (reloadError) {
          setError(reloadError instanceof Error ? reloadError.message : 'Rules changed and could not be reloaded.')
        }
      } else {
        setError(createError instanceof Error ? createError.message : 'Could not create the persistent rule.')
      }
      setBusy(false)
      return
    }

    setBusy(false)
    announce(`Persistent rule ${rule.id} created.`, 'success')
    try {
      await onCreated()
    } catch {
      announce(`Persistent rule ${rule.id} was created, but the view could not refresh.`, 'error')
    }
    onClose()
  }

  return (
    <div className="rule-dialog-backdrop" role="presentation" onMouseDown={(event) => {
      if (event.target === event.currentTarget && !busy) onClose()
    }}>
      <section className="rule-dialog" role="dialog" aria-modal="true" aria-labelledby="rule-dialog-title">
        <header>
          <div><span className="eyebrow">Persistent routing</span><h2 id="rule-dialog-title">Turn this flow into a rule.</h2></div>
          <button type="button" aria-label="Close rule creator" onClick={onClose} disabled={busy}>×</button>
        </header>

        <div className="rule-flow-summary">
          <code>{flow.id}</code>
          <strong>{flow.protocol.toUpperCase()} → {destination}</strong>
          <span>{flow.state} on channel {flow.channel} · {flow.mode}</span>
        </div>

        {!document && !error && <div className="rule-loading" aria-busy="true">Loading the authoritative rule revision…</div>}
        {document && !document.writable && <div className="inline-notice">This runtime is read-only. Start it with a writable owner-only configuration file to persist rules.</div>}
        {scope === 'exact' && exactRule && <div className="inline-notice">This flow is already pinned by <code>{exactRule.id}</code>.</div>}
        {error && <div className="rule-error" role="alert">{error}</div>}

        <form onSubmit={(event) => void submit(event)}>
          <fieldset disabled={!document || !document.writable || busy}>
            <label className="field">
              <span>Match scope</span>
              <select value={scope} onChange={(event) => setScope(event.target.value as RuleScope)}>
                <option value="exact">Exact flow · pinned precedence</option>
                <option value="destination_service">Latest destination service · {destination}</option>
                <option value="protocol">Entire protocol · {flow.protocol.toUpperCase()}</option>
              </select>
              <small>{scope === 'exact' ? 'Survives registry expiry and affects only this pseudonymous flow.' : scope === 'destination_service' ? 'Matches the latest packet direction, destination host, port, and protocol.' : 'Broad rule evaluated in ordered user-rule precedence.'}</small>
            </label>

            <div className="rule-form-grid">
              <label className="field"><span>Rule ID</span><input value={id} onChange={(event) => setID(event.target.value)} required spellCheck={false} /></label>
              <label className="field"><span>Display name</span><input value={name} onChange={(event) => setName(event.target.value)} /></label>
              <label className="field"><span>Action</span><select value={action.state} onChange={(event) => setAction(withActionState(action, event.target.value as RuleConfig['action']['state']))}><option value="play">Play</option><option value="monitor">Monitor</option><option value="ignore">Ignore</option></select></label>
              <label className="field"><span>MIDI channel</span><select value={action.channel} onChange={(event) => setAction({ ...action, channel: Number(event.target.value) })}><option value={0}>Inherit default</option>{Array.from({ length: 16 }, (_, index) => <option key={index + 1} value={index + 1}>Channel {index + 1}</option>)}</select></label>
              <MusicalIdentityFields action={action} fallbackMode={flow.mode} fallbackRoot={flow.root} onChange={setAction} />
            </div>

            <div className="rule-preview"><span>Authoritative match</span><code>{JSON.stringify(match)}</code></div>
          </fieldset>
          <footer>
            <span>{document ? `${document.rules.length} existing rules · revision ${document.revision.slice(0, 12)}` : 'Waiting for rule revision'}</span>
            <div><button className="text-button" type="button" onClick={onClose} disabled={busy}>Cancel</button><button className="primary-button" type="submit" disabled={!document?.writable || busy || (scope === 'exact' && Boolean(exactRule))}>{busy ? 'Creating…' : scope === 'exact' ? 'Pin flow' : 'Create rule'}</button></div>
          </footer>
        </form>
      </section>
    </div>
  )
}
