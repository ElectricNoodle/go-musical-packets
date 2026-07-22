import { useCallback, useEffect, useMemo, useState, type FormEvent, type KeyboardEvent } from 'react'
import { ApiError, type ManagementClient } from '../api/client'
import type { FlowPage, RuleConfig, RuleMatch, RulesDocument } from '../api/types'
import { MusicalIdentityFields, rootNames, withActionState } from './RuleMusicalFields'

interface RulesEditorProps {
  client: ManagementClient
  announce: (message: string, tone?: 'neutral' | 'success' | 'error') => void
  onPolicyChanged?: () => void | Promise<void>
}

interface EditState {
  rule: RuleConfig
  originalID?: string
}

function emptyRule(): RuleConfig {
  return { id: '', name: '', enabled: true, match: {}, action: { state: 'monitor', channel: 0 } }
}

function copyRule(rule: RuleConfig): RuleConfig {
  return JSON.parse(JSON.stringify(rule)) as RuleConfig
}

function meaningfulMatch(match: RuleMatch) {
  return Object.fromEntries(Object.entries(match).filter(([, value]) => value !== '' && value !== undefined && value !== null && (!Array.isArray(value) || value.length > 0)))
}

function matchSummary(rule: RuleConfig) {
  const match = meaningfulMatch(rule.match)
  const parts: string[] = []
  if (match.exact_flow_id) parts.push(`flow ${match.exact_flow_id}`)
  if (match.protocol) parts.push(String(match.protocol).toUpperCase())
  if (match.source_cidr) parts.push(`from ${match.source_cidr}`)
  if (match.destination_cidr) parts.push(`to ${match.destination_cidr}`)
  const source = match.source_ports as { minimum: number; maximum: number } | undefined
  const destination = match.destination_ports as { minimum: number; maximum: number } | undefined
  if (source) parts.push(`src ${source.minimum}${source.maximum === source.minimum ? '' : `–${source.maximum}`}`)
  if (destination) parts.push(`dst ${destination.minimum}${destination.maximum === destination.minimum ? '' : `–${destination.maximum}`}`)
  if (match.wire_size) parts.push('packet-size bounded')
  if (match.required_tcp_flags) parts.push(`flags ${(match.required_tcp_flags as string[]).join('+')}`)
  return parts.length > 0 ? parts.join(' · ') : 'all traffic'
}

function shadowedBy(rules: RuleConfig[], index: number) {
  const current = rules[index]
  if (!current?.enabled) return null
  const currentMatch = meaningfulMatch(current.match)
  const pinned = Boolean(currentMatch.exact_flow_id)
  for (let earlierIndex = 0; earlierIndex < index; earlierIndex++) {
    const earlier = rules[earlierIndex]
    if (!earlier?.enabled) continue
    const earlierMatch = meaningfulMatch(earlier.match)
    if (Boolean(earlierMatch.exact_flow_id) !== pinned) continue
    if (JSON.stringify(earlierMatch) === JSON.stringify(currentMatch)) return earlier.id
    if (!pinned && Object.keys(earlierMatch).length === 0) return earlier.id
    if (!pinned && earlierMatch.protocol && earlierMatch.protocol === currentMatch.protocol && Object.keys(earlierMatch).length === 1) return earlier.id
  }
  return null
}

function duplicateID(id: string, rules: RuleConfig[]) {
  const used = new Set(rules.map((rule) => rule.id))
  for (let suffix = 1; ; suffix++) {
    const candidate = `${id}-copy${suffix === 1 ? '' : `-${suffix}`}`
    if (!used.has(candidate)) return candidate
  }
}

export function RulesEditor({ client, announce, onPolicyChanged }: RulesEditorProps) {
  const [document, setDocument] = useState<RulesDocument | null>(null)
  const [flows, setFlows] = useState<FlowPage | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [editing, setEditing] = useState<EditState | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)
  const [transferOpen, setTransferOpen] = useState(false)
  const [transfer, setTransfer] = useState('')

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const [nextDocument, nextFlows] = await Promise.all([client.getRules(signal), client.getFlows(500, signal)])
      setDocument(nextDocument)
      setFlows(nextFlows)
      setError(null)
    } catch (loadError) {
      if (!signal?.aborted) setError(loadError instanceof Error ? loadError.message : 'Could not load persistent rules.')
    }
  }, [client])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  const refreshPolicy = async () => {
    try {
      await onPolicyChanged?.()
      setFlows(await client.getFlows(500))
    } catch {
      announce('Rules changed, but current flow counts could not be refreshed.', 'error')
    }
  }

  const conflict = async () => {
    try {
      const latest = await client.getRules()
      setDocument(latest)
      setError('Rules changed in another session. Review the refreshed order and retry your change.')
    } catch (reloadError) {
      setError(reloadError instanceof Error ? reloadError.message : 'The latest rules revision could not be loaded.')
    }
  }

  const mutate = async (message: string, operation: (current: RulesDocument) => Promise<RulesDocument>) => {
    if (!document) return false
    setBusy(true)
    setError(null)
    try {
      const next = await operation(document)
      setDocument(next)
      setConfirmDelete(null)
      announce(message, 'success')
      void refreshPolicy()
      return true
    } catch (mutationError) {
      if (mutationError instanceof ApiError && mutationError.status === 412) await conflict()
      else setError(mutationError instanceof Error ? mutationError.message : 'The rule change failed.')
      return false
    } finally {
      setBusy(false)
    }
  }

  const move = (index: number, offset: -1 | 1) => {
    if (!document) return
    const destination = index + offset
    if (destination < 0 || destination >= document.rules.length) return
    const order = document.rules.map((rule) => rule.id)
    const [id] = order.splice(index, 1)
    if (!id) return
    order.splice(destination, 0, id)
    void mutate(`Rule ${id} moved ${offset < 0 ? 'up' : 'down'}.`, (current) => client.reorderRules(order, current.etag))
  }

  const save = async (rule: RuleConfig, originalID?: string) => {
    const saved = await mutate(
      originalID ? `Rule ${rule.id} updated.` : `Rule ${rule.id} created.`,
      (current) => originalID ? client.replaceRule(originalID, rule, current.etag) : client.createRule(rule, current.etag),
    )
    if (saved) setEditing(null)
  }

  const importRules = () => {
    if (!document) return
    try {
      const parsed = JSON.parse(transfer) as unknown
      const rules = Array.isArray(parsed) ? parsed : typeof parsed === 'object' && parsed !== null && Array.isArray((parsed as { rules?: unknown }).rules) ? (parsed as { rules: unknown[] }).rules : null
      if (!rules) throw new Error('Import must be a rule array or an object containing a rules array.')
      void mutate(`Imported ${rules.length} persistent rules.`, (current) => client.replaceRules(rules as RuleConfig[], current.etag)).then((saved) => {
        if (saved) setTransferOpen(false)
      })
    } catch (parseError) {
      setError(parseError instanceof Error ? parseError.message : 'Rule import is not valid JSON.')
    }
  }

  const controlCounts = useMemo(() => {
    const counts = new Map<string, number>()
    flows?.flows.forEach((flow) => {
      if (flow.rule_id) counts.set(flow.rule_id, (counts.get(flow.rule_id) ?? 0) + 1)
    })
    return counts
  }, [flows])

  return (
    <section className="rules-editor" aria-labelledby="rules-title">
      <header className="rules-header">
        <div><span className="eyebrow">Persistent policy / first match wins within tier</span><h1 id="rules-title">Order the traffic. Shape the sound.</h1><p>Exact-flow pins are evaluated before broad rules. Every write uses the current strong revision and publishes the complete policy atomically.</p></div>
        <div className="rules-header__actions"><button className="secondary-button" type="button" onClick={() => { setTransfer(JSON.stringify({ rules: document?.rules ?? [] }, null, 2)); setTransferOpen(true) }} disabled={!document}>Import / export</button><button className="primary-button" type="button" onClick={() => setEditing({ rule: emptyRule() })} disabled={!document?.writable || busy}>New rule</button></div>
      </header>

      {error && <div className="explorer-error" role="alert"><span>{error}</span><button type="button" onClick={() => void load()}>Reload</button></div>}
      {document && !document.writable && <div className="inline-notice">This runtime is read-only. Rules can be inspected and exported, but not changed.</div>}

      <div className="rules-meta"><span>{document?.rules.length ?? 0} rules</span><span>{flows?.total ?? 0} retained flows</span><code>{document ? document.revision.slice(0, 12) : 'loading…'}</code></div>
      <div className="rule-list" aria-busy={!document}>
        {document?.rules.map((rule, index) => {
          const shadow = shadowedBy(document.rules, index)
          const pinned = Boolean(rule.match.exact_flow_id)
          return <article className={`rule-card${rule.enabled ? '' : ' rule-card--disabled'}`} key={rule.id} tabIndex={0} onKeyDown={(event: KeyboardEvent<HTMLElement>) => {
            if (!event.altKey) return
            if (event.key === 'ArrowUp') { event.preventDefault(); move(index, -1) }
            if (event.key === 'ArrowDown') { event.preventDefault(); move(index, 1) }
          }}>
            <div className="rule-order"><strong>{String(index + 1).padStart(2, '0')}</strong><button type="button" aria-label={`Move ${rule.id} up`} disabled={busy || index === 0 || !document.writable} onClick={() => move(index, -1)}>↑</button><button type="button" aria-label={`Move ${rule.id} down`} disabled={busy || index === document.rules.length - 1 || !document.writable} onClick={() => move(index, 1)}>↓</button></div>
            <div className="rule-card__body"><div className="rule-card__title"><span className={pinned ? 'rule-tier rule-tier--pinned' : 'rule-tier'}>{pinned ? 'Pinned' : 'User'}</span><div><strong>{rule.name || rule.id}</strong><code>{rule.id}</code></div></div><p>{matchSummary(rule)}</p>{shadow && <div className="shadow-warning">Potentially shadowed by <code>{shadow}</code></div>}</div>
            <div className="rule-outcome"><span className={`decision decision--${rule.action.state}`}>{rule.action.state}</span><small>{rule.action.channel === 0 ? 'Default channel' : `Channel ${rule.action.channel}`}</small><small>{rule.action.mode !== undefined && rule.action.root !== undefined ? `${rootNames[rule.action.root]} ${rule.action.mode}` : 'Automatic scale'}</small><small>{controlCounts.get(rule.id) ?? 0} currently controlled</small></div>
            <div className="rule-card__actions"><button type="button" disabled={busy || !document.writable} onClick={() => void mutate(`${rule.id} ${rule.enabled ? 'disabled' : 'enabled'}.`, (current) => client.replaceRule(rule.id, { ...copyRule(rule), enabled: !rule.enabled }, current.etag))}>{rule.enabled ? 'Disable' : 'Enable'}</button><button type="button" disabled={busy || !document.writable} onClick={() => setEditing({ rule: copyRule(rule), originalID: rule.id })}>Edit</button><button type="button" disabled={busy || !document.writable} onClick={() => { const duplicate = copyRule(rule); duplicate.id = duplicateID(rule.id, document.rules); duplicate.name = `${rule.name || rule.id} copy`; setEditing({ rule: duplicate }) }}>Duplicate</button><button className={confirmDelete === rule.id ? 'danger-action danger-action--armed' : 'danger-action'} type="button" disabled={busy || !document.writable} onClick={() => { if (confirmDelete !== rule.id) setConfirmDelete(rule.id); else void mutate(`Rule ${rule.id} deleted.`, (current) => client.deleteRule(rule.id, current.etag)) }}>{confirmDelete === rule.id ? 'Confirm delete' : 'Delete'}</button></div>
          </article>
        })}
        {document && document.rules.length === 0 && <div className="empty-flows"><strong>No persistent rules</strong><span>Create one here or from a live flow.</span></div>}
        {!document && !error && <div className="empty-flows"><strong>Loading ordered rules…</strong></div>}
      </div>

      {editing && <RuleEditDialog edit={editing} busy={busy} existingIDs={document?.rules.map((rule) => rule.id) ?? []} onSave={save} onClose={() => setEditing(null)} />}
      {transferOpen && <div className="rule-dialog-backdrop" role="presentation"><section className="rule-dialog transfer-dialog" role="dialog" aria-modal="true" aria-labelledby="transfer-title"><header><div><span className="eyebrow">Portable JSON</span><h2 id="transfer-title">Import or export the full order.</h2></div><button type="button" aria-label="Close import and export" onClick={() => setTransferOpen(false)}>×</button></header><div className="transfer-body"><p>Exported content contains rules only. Import replaces the complete collection in one revision-guarded transaction.</p><textarea aria-label="Rules JSON" value={transfer} onChange={(event) => setTransfer(event.target.value)} spellCheck={false} /><footer><button className="text-button" type="button" onClick={() => setTransfer(JSON.stringify({ rules: document?.rules ?? [] }, null, 2))}>Reset to current export</button><button className="primary-button" type="button" disabled={!document?.writable || busy} onClick={importRules}>Import atomically</button></footer></div></section></div>}
    </section>
  )
}

function RuleEditDialog({ edit, busy, existingIDs, onSave, onClose }: { edit: EditState; busy: boolean; existingIDs: string[]; onSave: (rule: RuleConfig, originalID?: string) => Promise<void>; onClose: () => void }) {
  const [rule, setRule] = useState(() => copyRule(edit.rule))
  const [matchText, setMatchText] = useState(() => JSON.stringify(edit.rule.match, null, 2))
  const [error, setError] = useState<string | null>(null)
  const [discardArmed, setDiscardArmed] = useState(false)
  const original = useMemo(() => JSON.stringify(edit.rule), [edit.rule])
  const dirty = JSON.stringify(rule) !== original || matchText !== JSON.stringify(edit.rule.match, null, 2)

  useEffect(() => {
    const protect = (event: BeforeUnloadEvent) => { if (dirty) event.preventDefault() }
    window.addEventListener('beforeunload', protect)
    return () => window.removeEventListener('beforeunload', protect)
  }, [dirty])

  const close = () => {
    if (dirty && !discardArmed) { setDiscardArmed(true); setError('Unsaved changes are present. Choose discard again to close.'); return }
    onClose()
  }

  const submit = (event: FormEvent) => {
    event.preventDefault()
    try {
      const match = JSON.parse(matchText) as unknown
      if (typeof match !== 'object' || match === null || Array.isArray(match)) throw new Error('Match must be a JSON object.')
      const next = { ...rule, id: rule.id.trim(), match: match as RuleMatch }
      if (!next.id) throw new Error('Rule ID is required.')
      if (!edit.originalID && existingIDs.includes(next.id)) throw new Error(`Rule ID ${next.id} already exists.`)
      setError(null)
      void onSave(next, edit.originalID)
    } catch (parseError) {
      setError(parseError instanceof Error ? parseError.message : 'Match JSON is invalid.')
    }
  }

  return (
    <div className="rule-dialog-backdrop" role="presentation">
      <section className="rule-dialog" role="dialog" aria-modal="true" aria-labelledby="edit-rule-title">
        <header>
          <div><span className="eyebrow">{edit.originalID ? 'Edit persistent rule' : 'New persistent rule'}</span><h2 id="edit-rule-title">Define match and outcome.</h2></div>
          <button type="button" aria-label="Close rule editor" onClick={close} disabled={busy}>×</button>
        </header>
        {error && <div className="rule-error" role="alert">{error}</div>}
        <form onSubmit={submit}>
          <fieldset disabled={busy}>
            <div className="rule-form-grid">
              <label className="field"><span>Rule ID</span><input value={rule.id} disabled={Boolean(edit.originalID)} onChange={(event) => setRule({ ...rule, id: event.target.value })} required /></label>
              <label className="field"><span>Display name</span><input value={rule.name} onChange={(event) => setRule({ ...rule, name: event.target.value })} /></label>
              <label className="field"><span>Action</span><select value={rule.action.state} onChange={(event) => setRule({ ...rule, action: withActionState(rule.action, event.target.value as RuleConfig['action']['state']) })}><option value="play">Play</option><option value="monitor">Monitor</option><option value="ignore">Ignore</option></select></label>
              <label className="field"><span>MIDI channel</span><select value={rule.action.channel} onChange={(event) => setRule({ ...rule, action: { ...rule.action, channel: Number(event.target.value) } })}><option value={0}>Inherit default</option>{Array.from({ length: 16 }, (_, index) => <option key={index + 1} value={index + 1}>Channel {index + 1}</option>)}</select></label>
              <MusicalIdentityFields action={rule.action} onChange={(action) => setRule({ ...rule, action })} />
            </div>
            <label className="toggle-card"><span><strong>Enabled</strong><small>Disabled rules remain ordered but do not evaluate.</small></span><input type="checkbox" checked={rule.enabled} onChange={(event) => setRule({ ...rule, enabled: event.target.checked })} /><i aria-hidden="true" /></label>
            <label className="field"><span>Match JSON</span><textarea className="match-editor" value={matchText} onChange={(event) => setMatchText(event.target.value)} spellCheck={false} /><small>Supported fields: exact_flow_id, source_cidr, destination_cidr, protocol, port ranges, wire_size, and required_tcp_flags.</small></label>
          </fieldset>
          <footer><span>{dirty ? 'Unsaved changes' : 'No changes yet'}</span><div><button className="text-button" type="button" onClick={close}>{discardArmed ? 'Discard changes' : 'Cancel'}</button><button className="primary-button" type="submit" disabled={busy}>{edit.originalID ? 'Save rule' : 'Create rule'}</button></div></footer>
        </form>
      </section>
    </div>
  )
}
