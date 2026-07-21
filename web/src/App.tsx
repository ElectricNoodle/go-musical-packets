import { useCallback, useEffect, useMemo, useState } from 'react'
import { createManagementClient, type ManagementClient } from './api/client'
import type { RuntimeSnapshot } from './api/types'
import { SetupAssistant } from './components/SetupAssistant'
import { StatusRail } from './components/StatusRail'

interface Notice {
  message: string
  tone: 'neutral' | 'success' | 'error'
}

interface AppProps {
  client?: ManagementClient
}

export default function App({ client: suppliedClient }: AppProps) {
  const client = useMemo(() => suppliedClient ?? createManagementClient(), [suppliedClient])
  const [snapshot, setSnapshot] = useState<RuntimeSnapshot | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<Notice | null>(null)
  const [panicBusy, setPanicBusy] = useState(false)

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const [status, config, interfaces, midi] = await Promise.all([
        client.getStatus(signal),
        client.getConfig(signal),
        client.getInterfaces(signal),
        client.getMIDI(signal),
      ])
      setSnapshot({ status, config, interfaces, midi })
      setError(null)
    } catch (loadError) {
      if (signal?.aborted) return
      setError(loadError instanceof Error ? loadError.message : 'Could not load the management API.')
    }
  }, [client])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  const announce = (message: string, tone: Notice['tone'] = 'neutral') => {
    setNotice({ message, tone })
  }

  const panic = async () => {
    setPanicBusy(true)
    try {
      await client.panicMIDI()
      announce('All Notes Off sent on every MIDI channel.', 'success')
    } catch (panicError) {
      announce(panicError instanceof Error ? panicError.message : 'MIDI panic failed.', 'error')
    } finally {
      setPanicBusy(false)
    }
  }

  if (error) {
    return (
      <main className="boot-state">
        <div className="brand-mark" aria-hidden="true"><i /><i /><i /><i /></div>
        <span className="eyebrow">Connection interrupted</span>
        <h1>The management surface is unavailable.</h1>
        <p>{error}</p>
        <button className="primary-button" type="button" onClick={() => void load()}>Try again</button>
      </main>
    )
  }

  if (!snapshot) {
    return (
      <main className="boot-state" aria-busy="true">
        <div className="brand-mark brand-mark--pulse" aria-hidden="true"><i /><i /><i /><i /></div>
        <span className="eyebrow">Musical Packets</span>
        <h1>Listening for the runtime…</h1>
      </main>
    )
  }

  return (
    <div className="app-shell">
      <header className="topbar">
        <a className="brand" href="/" aria-label="Musical Packets home">
          <div className="brand-mark" aria-hidden="true"><i /><i /><i /><i /></div>
          <span><strong>Musical</strong> Packets</span>
        </a>
        <div className="topbar__meta">
          <span className="live-dot" aria-hidden="true" />
          <span>{snapshot.config.config.instance.id}</span>
          <code>{snapshot.config.config.instance.role}</code>
        </div>
      </header>

      <StatusRail snapshot={snapshot} onPanic={panic} busy={panicBusy} />
      <main className="workspace">
        {snapshot.status.warning && <div className="warning-banner" role="status">{snapshot.status.warning}</div>}
        <SetupAssistant key={snapshot.config.revision} client={client} snapshot={snapshot} onApplied={() => load()} announce={announce} />
      </main>

      {notice && (
        <div className={`toast toast--${notice.tone}`} role={notice.tone === 'error' ? 'alert' : 'status'}>
          <span>{notice.message}</span>
          <button type="button" onClick={() => setNotice(null)} aria-label="Dismiss notification">×</button>
        </div>
      )}
    </div>
  )
}
