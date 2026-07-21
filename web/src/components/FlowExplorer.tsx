import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ManagementClient } from '../api/client'
import type { FlowOverlay, FlowPage, FlowSnapshot } from '../api/types'

type SortKey = 'last_seen' | 'packets' | 'bytes' | 'rate' | 'protocol' | 'endpoint'
type SortDirection = 'ascending' | 'descending'

interface FlowRate {
  packets: number
  bytes: number
}

interface FlowExplorerProps {
  client: ManagementClient
  announce: (message: string, tone?: 'neutral' | 'success' | 'error') => void
}

const flowLimit = 500
const pollInterval = 3000
const number = new Intl.NumberFormat()
const rateNumber = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 })
const rootNames = ['C', 'C♯', 'D', 'E♭', 'E', 'F', 'F♯', 'G', 'A♭', 'A', 'B♭', 'B']

function endpoint(flow: FlowSnapshot, side: 'endpoint_a' | 'endpoint_b') {
  const value = flow[side]
  const address = value.address.includes(':') ? `[${value.address}]` : value.address
  return value.port === 0 ? address : `${address}:${value.port}`
}

function compare(left: FlowSnapshot, right: FlowSnapshot, key: SortKey, rates: Map<string, FlowRate>) {
  switch (key) {
    case 'packets': return left.packets - right.packets
    case 'bytes': return left.bytes - right.bytes
    case 'rate': return (rates.get(left.id)?.packets ?? -1) - (rates.get(right.id)?.packets ?? -1)
    case 'protocol': return left.protocol.localeCompare(right.protocol)
    case 'endpoint': return endpoint(left, 'endpoint_a').localeCompare(endpoint(right, 'endpoint_a'))
    case 'last_seen': return Date.parse(left.last_seen) - Date.parse(right.last_seen)
  }
}

function byteRate(value: number) {
  if (value >= 1_000_000) return `${rateNumber.format(value / 1_000_000)} MB/s`
  if (value >= 1_000) return `${rateNumber.format(value / 1_000)} kB/s`
  return `${rateNumber.format(value)} B/s`
}

function applyOverlay(page: FlowPage, overlay: FlowOverlay): FlowPage {
  const muted = new Set(overlay.muted)
  const soloed = new Set(overlay.soloed)
  return {
    ...page,
    overlay,
    flows: page.flows.map((flow) => ({ ...flow, muted: muted.has(flow.id), soloed: soloed.has(flow.id) })),
  }
}

export function FlowExplorer({ client, announce }: FlowExplorerProps) {
  const [page, setPage] = useState<FlowPage | null>(null)
  const [query, setQuery] = useState('')
  const [sortKey, setSortKey] = useState<SortKey>('last_seen')
  const [sortDirection, setSortDirection] = useState<SortDirection>('descending')
  const [selected, setSelected] = useState<Set<string>>(() => new Set())
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null)
  const [rates, setRates] = useState<Map<string, FlowRate>>(() => new Map())
  const [annotationsPending, setAnnotationsPending] = useState(false)
  const loading = useRef(false)
  const loadQueued = useRef(false)
  const overlayGeneration = useRef(0)
  const previousSample = useRef<{ at: number; counters: Map<string, { packets: number; bytes: number }> } | null>(null)

  const load = useCallback(async (signal?: AbortSignal) => {
    if (loading.current) {
      loadQueued.current = true
      return
    }
    loading.current = true
    try {
      do {
        loadQueued.current = false
        const generation = overlayGeneration.current
        try {
          const next = await client.getFlows(flowLimit, signal)
          if (generation !== overlayGeneration.current) continue
          const sampledAt = Date.now()
          const previous = previousSample.current
          const nextRates = new Map<string, FlowRate>()
          if (previous && sampledAt > previous.at) {
            const elapsed = (sampledAt - previous.at) / 1000
            next.flows.forEach((flow) => {
              const before = previous.counters.get(flow.id)
              if (before && flow.packets >= before.packets && flow.bytes >= before.bytes) {
                nextRates.set(flow.id, {
                  packets: (flow.packets - before.packets) / elapsed,
                  bytes: (flow.bytes - before.bytes) / elapsed,
                })
              }
            })
          }
          previousSample.current = {
            at: sampledAt,
            counters: new Map(next.flows.map((flow) => [flow.id, { packets: flow.packets, bytes: flow.bytes }])),
          }
          setRates(nextRates)
          setPage(next)
          setSelected((current) => {
            const retained = new Set(next.flows.map((flow) => flow.id))
            return new Set([...current].filter((id) => retained.has(id)))
          })
          setUpdatedAt(new Date())
          setAnnotationsPending(false)
          setError(null)
        } catch (loadError) {
          if (signal?.aborted) break
          setError(loadError instanceof Error ? loadError.message : 'Could not load the live flow registry.')
        }
      } while (loadQueued.current && !signal?.aborted)
    } finally {
      loading.current = false
    }
  }, [client])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void load(controller.signal)
    }, pollInterval)
    return () => {
      controller.abort()
      window.clearInterval(timer)
    }
  }, [load])

  const visibleFlows = useMemo(() => {
    if (!page) return []
    const needle = query.trim().toLocaleLowerCase()
    const filtered = needle === '' ? page.flows : page.flows.filter((flow) => [
      flow.id,
      flow.protocol,
      endpoint(flow, 'endpoint_a'),
      endpoint(flow, 'endpoint_b'),
      flow.state,
      flow.mode,
      flow.rule_tier,
      flow.rule_id ?? '',
      String(flow.channel),
    ].some((value) => value.toLocaleLowerCase().includes(needle)))
    return [...filtered].sort((left, right) => {
      const order = compare(left, right, sortKey, rates)
      return sortDirection === 'ascending' ? order : -order
    })
  }, [page, query, rates, sortDirection, sortKey])

  const changeSort = (key: SortKey) => {
    if (key === sortKey) {
      setSortDirection((current) => current === 'ascending' ? 'descending' : 'ascending')
    } else {
      setSortKey(key)
      setSortDirection(key === 'protocol' || key === 'endpoint' ? 'ascending' : 'descending')
    }
  }

  const setOverlay = async (kind: 'mute' | 'solo', flowIDs: string[]) => {
    if (!page) return
    overlayGeneration.current += 1
    setBusy(true)
    try {
      const overlay = kind === 'mute'
        ? await client.setMutedFlows(flowIDs)
        : await client.setSoloedFlows(flowIDs)
      overlayGeneration.current += 1
      setPage((current) => current ? applyOverlay(current, overlay) : current)
      setAnnotationsPending(true)
      announce(`${kind === 'mute' ? 'Mute' : 'Solo'} state updated for ${flowIDs.length} flow${flowIDs.length === 1 ? '' : 's'}.`, 'success')
      void load()
    } catch (mutationError) {
      overlayGeneration.current += 1
      setAnnotationsPending(false)
      announce(mutationError instanceof Error ? mutationError.message : `Could not update ${kind} state.`, 'error')
    } finally {
      setBusy(false)
    }
  }

  const toggleFlow = (kind: 'mute' | 'solo', flow: FlowSnapshot) => {
    if (!page) return
    const values = new Set(kind === 'mute' ? page.overlay.muted : page.overlay.soloed)
    if (values.has(flow.id)) values.delete(flow.id)
    else values.add(flow.id)
    void setOverlay(kind, [...values].sort())
  }

  const clearTemporaryState = async () => {
    overlayGeneration.current += 1
    setAnnotationsPending(true)
    setBusy(true)
    try {
      await client.setMutedFlows([])
      const overlay = await client.setSoloedFlows([])
      overlayGeneration.current += 1
      setPage((current) => current ? applyOverlay(current, overlay) : current)
      announce('All temporary mute and solo state cleared.', 'success')
      void load()
    } catch (mutationError) {
      overlayGeneration.current += 1
      announce(mutationError instanceof Error ? mutationError.message : 'Could not clear temporary flow state.', 'error')
      await load()
    } finally {
      setBusy(false)
    }
  }

  const selectedIDs = [...selected].sort()
  const allVisibleSelected = visibleFlows.length > 0 && visibleFlows.every((flow) => selected.has(flow.id))

  const toggleVisible = () => {
    setSelected((current) => {
      const next = new Set(current)
      if (allVisibleSelected) visibleFlows.forEach((flow) => next.delete(flow.id))
      else visibleFlows.forEach((flow) => next.add(flow.id))
      return next
    })
  }

  return (
    <section className="flow-explorer" aria-labelledby="flow-explorer-title">
      <header className="explorer-header">
        <div>
          <span className="eyebrow">Live registry / bounded to {number.format(flowLimit)}</span>
          <h1 id="flow-explorer-title">Find the traffic worth hearing.</h1>
          <p>Search the canonical flow registry, inspect direction and activity, then apply temporary mute or solo state without interrupting capture.</p>
        </div>
        <div className="registry-readout" aria-live="polite">
          <strong>{page ? number.format(page.total) : '—'}</strong>
          <span>active flows</span>
          <small>{updatedAt ? `Updated ${updatedAt.toLocaleTimeString()}` : 'Connecting…'}</small>
          <button type="button" onClick={() => void load()}>Refresh now</button>
        </div>
      </header>

      <div className="explorer-toolbar">
        <label className="search-field">
          <span>Search flows</span>
          <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Address, port, protocol, or flow ID" type="search" />
        </label>
        <div className="selection-actions" aria-label="Selected flow actions">
          <span>{selected.size} selected</span>
          <button type="button" disabled={busy || selected.size === 0} onClick={() => void setOverlay('mute', [...new Set([...(page?.overlay.muted ?? []), ...selectedIDs])].sort())}>Mute selected</button>
          <button type="button" disabled={busy || selected.size === 0} onClick={() => void setOverlay('solo', selectedIDs)}>Solo selected</button>
          <button type="button" disabled={busy || selected.size === 0} onClick={() => setSelected(new Set())}>Clear selection</button>
        </div>
      </div>

      {error && (
        <div className="explorer-error" role="alert">
          <span>{error}</span>
          <button type="button" onClick={() => void load()} disabled={busy}>Try again</button>
        </div>
      )}

      <div className="flow-table-frame" aria-busy={!page}>
        <table className="flow-table">
          <thead>
            <tr>
              <th className="selection-column">
                <input type="checkbox" aria-label="Select all visible flows" checked={allVisibleSelected} onChange={toggleVisible} disabled={visibleFlows.length === 0} />
              </th>
              <th aria-sort={sortKey === 'protocol' ? sortDirection : 'none'}><button type="button" onClick={() => changeSort('protocol')}>Protocol</button></th>
              <th aria-sort={sortKey === 'endpoint' ? sortDirection : 'none'}><button type="button" onClick={() => changeSort('endpoint')}>Canonical endpoints</button></th>
              <th aria-sort={sortKey === 'packets' ? sortDirection : 'none'}><button type="button" onClick={() => changeSort('packets')}>Packets</button></th>
              <th aria-sort={sortKey === 'bytes' ? sortDirection : 'none'}><button type="button" onClick={() => changeSort('bytes')}>Bytes</button></th>
              <th aria-sort={sortKey === 'rate' ? sortDirection : 'none'}><button type="button" onClick={() => changeSort('rate')}>Observed rate</button></th>
              <th aria-sort={sortKey === 'last_seen' ? sortDirection : 'none'}><button type="button" onClick={() => changeSort('last_seen')}>Last activity</button></th>
              <th>Musical identity</th>
              <th>Decision</th>
              <th>Temporary state</th>
            </tr>
          </thead>
          <tbody>
            {visibleFlows.map((flow) => (
              <tr key={flow.id} className={flow.soloed ? 'flow-row flow-row--soloed' : flow.muted ? 'flow-row flow-row--muted' : 'flow-row'}>
                <td className="selection-column"><input type="checkbox" aria-label={`Select flow ${flow.id}`} checked={selected.has(flow.id)} onChange={() => setSelected((current) => {
                  const next = new Set(current)
                  if (next.has(flow.id)) next.delete(flow.id)
                  else next.add(flow.id)
                  return next
                })} /></td>
                <td><span className={`protocol protocol--${flow.protocol.toLocaleLowerCase()}`}>{flow.protocol}</span><code className="flow-id">{flow.id}</code></td>
                <td><span className="endpoint-pair"><code>{endpoint(flow, 'endpoint_a')}</code><span aria-hidden="true">⇄</span><code>{endpoint(flow, 'endpoint_b')}</code></span><small>{number.format(flow.packets_a_to_b)} → / {number.format(flow.packets_b_to_a)} ←</small></td>
                <td className="numeric">{number.format(flow.packets)}</td>
                <td className="numeric">{number.format(flow.bytes)}</td>
                <td>{rates.has(flow.id) ? <><strong className="flow-rate">{rateNumber.format(rates.get(flow.id)?.packets ?? 0)} pkt/s</strong><small>{byteRate(rates.get(flow.id)?.bytes ?? 0)}</small></> : <span className="rate-pending">Sampling…</span>}</td>
                <td><time dateTime={flow.last_seen}>{new Date(flow.last_seen).toLocaleTimeString()}</time><small>{new Date(flow.first_seen).toLocaleDateString()}</small></td>
                <td><span className="musical-identity"><strong>{rootNames[flow.root] ?? `PC ${flow.root}`} {flow.mode}</strong><small>Channel {flow.channel}</small></span></td>
                <td>{annotationsPending ? <span className="decision decision--pending">refreshing</span> : <><span className={`decision decision--${flow.state}`}>{flow.state}</span><small>{flow.rule_id ? `${flow.rule_tier} · ${flow.rule_id}` : flow.rule_tier}</small></>}</td>
                <td><div className="flow-actions"><button type="button" aria-pressed={flow.muted} disabled={busy} onClick={() => toggleFlow('mute', flow)}>{flow.muted ? 'Unmute' : 'Mute'}</button><button type="button" aria-pressed={flow.soloed} disabled={busy} onClick={() => toggleFlow('solo', flow)}>{flow.soloed ? 'Unsolo' : 'Solo'}</button></div></td>
              </tr>
            ))}
          </tbody>
        </table>
        {page && visibleFlows.length === 0 && <div className="empty-flows"><strong>{query ? 'No matching flows' : 'No traffic observed yet'}</strong><span>{query ? 'Try a broader address, port, protocol, or ID.' : 'Captured flows will appear here automatically.'}</span></div>}
        {!page && !error && <div className="empty-flows"><strong>Reading the flow registry…</strong></div>}
      </div>

      <footer className="explorer-footer">
        <span>Showing {number.format(visibleFlows.length)} of {number.format(page?.total ?? 0)} flows{page?.truncated ? ' · registry window truncated' : ''}</span>
        <button type="button" disabled={busy || !page || (page.overlay.muted.length === 0 && page.overlay.soloed.length === 0)} onClick={() => void clearTemporaryState()}>Clear all temporary state</button>
      </footer>
    </section>
  )
}
