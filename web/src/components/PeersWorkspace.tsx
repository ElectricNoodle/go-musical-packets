import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ManagementClient } from '../api/client'
import type { ConnectedNode, PeersDocument } from '../api/types'

interface PeersWorkspaceProps {
  client: ManagementClient
}

const number = new Intl.NumberFormat()
const rate = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 })

function age(value?: string) {
  if (!value) return 'never'
  const elapsed = Date.now() - Date.parse(value)
  if (elapsed < -1_000) {
    const future = Math.abs(elapsed)
    if (future < 60_000) return `in ${Math.ceil(future / 1_000)}s`
    return `in ${Math.ceil(future / 60_000)}m`
  }
  if (elapsed < 1_000) return 'just now'
  if (elapsed < 60_000) return `${Math.floor(elapsed / 1_000)}s ago`
  if (elapsed < 3_600_000) return `${Math.floor(elapsed / 60_000)}m ago`
  return `${Math.floor(elapsed / 3_600_000)}h ago`
}

function stateClass(state: string) {
  return state === 'connected' ? 'peer-state peer-state--good' : state === 'connecting' ? 'peer-state peer-state--warn' : 'peer-state'
}

function ChannelBadges({ channels }: { channels: number[] }) {
  return <div className="peer-channels" aria-label={channels.length ? `Active channels ${channels.join(', ')}` : 'No active channels'}>{channels.length ? channels.map((channel) => <span key={channel}>CH {String(channel).padStart(2, '0')}</span>) : <small>None observed</small>}</div>
}

function EdgePeer({ document }: { document: PeersDocument }) {
  const peer = document.outbound
  if (!peer) return <div className="peer-empty"><strong>Edge transport is not active.</strong><span>Enable peer transport and provide a host URL in the next-start configuration.</span></div>
  const fill = peer.queue.capacity > 0 ? Math.min(100, peer.queue.depth / peer.queue.capacity * 100) : 0
  return (
    <div className="edge-peer-grid">
      <article className="peer-destination">
        <div className="peer-card-heading"><span className={stateClass(peer.state)}><i />{peer.state}</span><code>{peer.protocol_version || 'awaiting handshake'}</code></div>
        <span className="eyebrow">Configured destination</span>
        <h2>{peer.remote_instance || 'Host not yet negotiated'}</h2>
        <code className="peer-target">{peer.target}</code>
        <dl className="peer-facts">
          <div><dt>Mapping</dt><dd>{peer.mapping_version || '—'}</dd></div>
          <div><dt>Round trip</dt><dd>{peer.rtt_ms > 0 ? `${peer.rtt_ms} ms` : '—'}</dd></div>
          <div><dt>Connected</dt><dd>{age(peer.connected_at)}</dd></div>
          <div><dt>Last send</dt><dd>{age(peer.last_sent_at)}</dd></div>
        </dl>
        {peer.last_error && <p className="peer-error">{peer.last_error}</p>}
      </article>
      <section className="peer-activity">
        <div className="panel-heading"><div><span>Bounded delivery</span><strong>Outgoing activity</strong></div><small>{rate.format(peer.send_rate)} notes/s</small></div>
        <div className="peer-queue"><div><span>Queue</span><strong>{number.format(peer.queue.depth)} / {number.format(peer.queue.capacity)}</strong></div><i><b style={{ width: `${fill}%` }} /></i></div>
        <div className="peer-counters"><div><strong>{number.format(peer.sent_total)}</strong><span>Sent</span></div><div><strong>{number.format(peer.dropped_full)}</strong><span>Queue drops</span></div><div><strong>{number.format(peer.dropped_stale)}</strong><span>Stale drops</span></div><div><strong>{number.format(peer.reconnects)}</strong><span>Reconnects</span></div></div>
        <div className="peer-channel-block"><span className="eyebrow">Channels sent</span><ChannelBadges channels={peer.active_channels} /></div>
        {peer.state === 'backoff' && <p className="peer-retry">Next retry {age(peer.next_retry_at)}</p>}
      </section>
    </div>
  )
}

function HostNode({ node }: { node: ConnectedNode }) {
  return (
    <article className={node.state === 'connected' ? 'node-card' : 'node-card node-card--offline'}>
      <header><div><span className={stateClass(node.state)}><i />{node.state}</span><h2>{node.instance_id}</h2></div><a href={`/viewer?origin=${encodeURIComponent(node.instance_id)}`}>View notes →</a></header>
      <code>{node.remote_address}</code>
      <dl className="peer-facts"><div><dt>Protocol</dt><dd>{node.protocol_version}</dd></div><div><dt>Mapping</dt><dd>{node.mapping_version}</dd></div><div><dt>Connected</dt><dd>{age(node.connected_at)}</dd></div><div><dt>Last activity</dt><dd>{age(node.last_seen_at)}</dd></div></dl>
      <div className="node-rate"><strong>{rate.format(node.note_rate)}</strong><span>accepted notes / second</span></div>
      <ChannelBadges channels={node.active_channels} />
      <div className="node-totals"><span><b>{number.format(node.accepted_total)}</b> accepted</span><span><b>{number.format(node.rejected_total)}</b> rejected</span><span><b>{number.format(node.duplicate_total)}</b> duplicate</span><span><b>{number.format(node.stale_total)}</b> stale</span></div>
      <small>{node.authenticated ? 'Bearer authenticated' : 'Authentication unavailable'}</small>
    </article>
  )
}

function HostPeers({ nodes }: { nodes: ConnectedNode[] }) {
  const [query, setQuery] = useState('')
  const visible = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase()
    if (!normalized) return nodes
    return nodes.filter((node) => `${node.instance_id} ${node.remote_address} ${node.state} ${node.active_channels.join(' ')}`.toLocaleLowerCase().includes(normalized))
  }, [nodes, query])
  const connected = nodes.filter((node) => node.state === 'connected').length
  return (
    <>
      <div className="peer-toolbar"><label className="search-field"><span>Search nodes</span><input type="search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Identity, endpoint, state, or channel" /></label><div><strong>{connected}</strong><span>connected · {nodes.length - connected} recent</span></div></div>
      <div className="node-grid">{visible.map((node) => <HostNode key={node.instance_id} node={node} />)}</div>
      {visible.length === 0 && <div className="peer-empty"><strong>{query ? 'No matching nodes' : 'No edge nodes connected'}</strong><span>{query ? 'Try a broader identity, endpoint, state, or channel.' : 'Authenticated edge nodes will appear here as soon as they complete the peer handshake.'}</span></div>}
    </>
  )
}

export function PeersWorkspace({ client }: PeersWorkspaceProps) {
  const [document, setDocument] = useState<PeersDocument | null>(null)
  const [error, setError] = useState<string | null>(null)
  const loading = useRef(false)
  const load = useCallback(async (signal?: AbortSignal) => {
    if (loading.current) return
    loading.current = true
    try {
      setDocument(await client.getPeers(signal))
      setError(null)
    } catch (loadError) {
      if (!signal?.aborted) setError(loadError instanceof Error ? loadError.message : 'Could not load peer status.')
    } finally {
      loading.current = false
    }
  }, [client])
  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    const timer = window.setInterval(() => { if (window.document.visibilityState === 'visible') void load(controller.signal) }, 2_000)
    return () => { controller.abort(); window.clearInterval(timer) }
  }, [load])

  return (
    <section className="peers-workspace" aria-labelledby="peers-title">
      <header className="peers-header"><div><span className="eyebrow">Stage 14 · composed peer runtime</span><h1 id="peers-title">{document?.role === 'host' ? 'Connected instruments, clearly heard.' : document?.role === 'edge' ? 'Know exactly where the music goes.' : 'Peer transport at a glance.'}</h1><p>{document?.role === 'host' ? 'Current and recent edge nodes are bounded, authenticated, and observed without turning identities into metric labels.' : document?.role === 'edge' ? 'The configured destination, negotiated host, queue pressure, and delivery state remain visible without exposing credentials.' : 'This standalone runtime plays locally and does not open a peer transport.'}</p></div><button type="button" className="secondary-button" onClick={() => void load()}>Refresh now</button></header>
      {error && <div className="explorer-error" role="alert"><span>{error}</span><button type="button" onClick={() => void load()}>Try again</button></div>}
      {!document && !error && <div className="peer-empty" aria-busy="true"><strong>Reading peer runtime state…</strong></div>}
      {document?.role === 'edge' && <EdgePeer document={document} />}
      {document?.role === 'host' && document.enabled && <HostPeers nodes={document.nodes} />}
      {document?.role === 'host' && !document.enabled && <div className="peer-empty"><strong>Host transport is disabled.</strong><span>Enable peer transport in Setup and save the configuration for the next process start.</span></div>}
      {document?.role === 'standalone' && <div className="peer-empty"><strong>No peer role is active.</strong><span>Choose an edge or host role in Setup and save it for the next process start.</span></div>}
    </section>
  )
}
