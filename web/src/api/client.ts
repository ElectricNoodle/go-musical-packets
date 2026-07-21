import { parse, stringify } from 'yaml'
import type {
  ConfigDocument,
  Configuration,
  FlowOverlay,
  FlowPage,
  InterfacesDocument,
  MIDIDevicesDocument,
  ProblemDocument,
  RuleConfig,
  RulesDocument,
  Status,
  Validation,
} from './types'

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly fields: string[]

  constructor(problem: ProblemDocument) {
    super(problem.detail)
    this.name = 'ApiError'
    this.status = problem.status
    this.code = problem.code
    this.fields = problem.fields ?? []
  }
}

export interface ManagementClient {
  getStatus(signal?: AbortSignal): Promise<Status>
  getConfig(signal?: AbortSignal): Promise<ConfigDocument>
  getInterfaces(signal?: AbortSignal): Promise<InterfacesDocument>
  getMIDI(signal?: AbortSignal): Promise<MIDIDevicesDocument>
  validateConfig(config: Configuration, signal?: AbortSignal): Promise<Validation>
  updateConfig(config: Configuration, revision: string, signal?: AbortSignal): Promise<ConfigDocument>
  auditionMIDI(channel: number, signal?: AbortSignal): Promise<void>
  panicMIDI(signal?: AbortSignal): Promise<void>
  getFlows(limit?: number, signal?: AbortSignal): Promise<FlowPage>
  setMutedFlows(flowIDs: string[], signal?: AbortSignal): Promise<FlowOverlay>
  setSoloedFlows(flowIDs: string[], signal?: AbortSignal): Promise<FlowOverlay>
  getRules(signal?: AbortSignal): Promise<RulesDocument>
  createRule(rule: RuleConfig, revision: string, signal?: AbortSignal): Promise<RulesDocument>
}

const yamlHeaders = {
  'Content-Type': 'application/yaml',
} as const

export function createManagementClient(fetcher: typeof fetch = fetch): ManagementClient {
  const request = async (path: string, init: RequestInit = {}): Promise<Response> => {
    const response = await fetcher(path, {
      credentials: 'same-origin',
      cache: 'no-store',
      ...init,
    })
    if (!response.ok) {
      let problem: ProblemDocument = {
        status: response.status,
        code: 'request_failed',
        detail: `Management request failed with HTTP ${response.status}.`,
      }
      try {
        problem = (await response.json()) as ProblemDocument
      } catch {
        // Keep the bounded fallback; malformed error bodies are not trusted.
      }
      throw new ApiError(problem)
    }
    return response
  }

  const json = async <T>(path: string, init: RequestInit = {}): Promise<T> => {
    const response = await request(path, init)
    return (await response.json()) as T
  }

  const configDocument = async (response: Response): Promise<ConfigDocument> => {
    const revision = response.headers.get('ETag')
    if (!revision) {
      throw new Error('Configuration response did not include an ETag revision.')
    }
    return {
      config: parse(await response.text()) as Configuration,
      revision,
    }
  }

  const rulesDocument = async (response: Response): Promise<RulesDocument> => {
    const etag = response.headers.get('ETag')
    if (!etag) {
      throw new Error('Rules response did not include an ETag revision.')
    }
    const document = await response.json() as Omit<RulesDocument, 'etag'>
    return { ...document, etag }
  }

  return {
    getStatus: (signal) => json<Status>('/api/v1/status', { signal }),
    getConfig: async (signal) => configDocument(await request('/api/v1/config', { signal })),
    getInterfaces: (signal) => json<InterfacesDocument>('/api/v1/interfaces', { signal }),
    getMIDI: (signal) => json<MIDIDevicesDocument>('/api/v1/midi/devices', { signal }),
    validateConfig: (config, signal) =>
      json<Validation>('/api/v1/config/validate', {
        method: 'POST',
        headers: yamlHeaders,
        body: stringify(config),
        signal,
      }),
    updateConfig: async (config, revision, signal) =>
      configDocument(
        await request('/api/v1/config', {
          method: 'PUT',
          headers: { ...yamlHeaders, 'If-Match': revision },
          body: stringify(config),
          signal,
        }),
      ),
    auditionMIDI: async (channel, signal) => {
      await request('/api/v1/midi/audition', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ channel, note: 60, velocity: 96, duration_ms: 300 }),
        signal,
      })
    },
    panicMIDI: async (signal) => {
      await request('/api/v1/midi/panic', { method: 'POST', signal })
    },
    getFlows: (limit = 500, signal) => json<FlowPage>(`/api/v1/flows?limit=${encodeURIComponent(limit)}`, { signal }),
    setMutedFlows: (flowIDs, signal) =>
      json<FlowOverlay>('/api/v1/flows/mute', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ flow_ids: flowIDs }),
        signal,
      }),
    setSoloedFlows: (flowIDs, signal) =>
      json<FlowOverlay>('/api/v1/flows/solo', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ flow_ids: flowIDs }),
        signal,
      }),
    getRules: async (signal) => rulesDocument(await request('/api/v1/rules', { signal })),
    createRule: async (rule, revision, signal) => rulesDocument(await request('/api/v1/rules', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'If-Match': revision },
      body: JSON.stringify(rule),
      signal,
    })),
  }
}
