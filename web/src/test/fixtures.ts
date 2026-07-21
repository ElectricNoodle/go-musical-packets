import { vi } from 'vitest'
import type { Configuration, FlowPage, RuntimeSnapshot } from '../api/types'
import type { ManagementClient } from '../api/client'

export const configuration: Configuration = {
  instance: { id: 'studio-node', role: 'standalone' },
  capture: { enabled: true, interface: 'auto', bpf: 'ip or ip6', snapshot_length: 65535, promiscuous: true },
  mapping: {
    version: 'flow-mode-v1', seed: '<write-only>', default_state: 'monitor', default_channel: 1,
    minimum_note: 36, maximum_note: 96, minimum_duration: '50ms', maximum_duration: '2s',
  },
  performance: {
    packet_queue_capacity: 4096, note_queue_capacity: 1024, ui_queue_capacity: 512,
    flow_registry_capacity: 10000, flow_ttl: '5m', maximum_notes_per_second: 100,
    maximum_polyphony: 32, minimum_retrigger_interval: '10ms',
  },
  midi: { enabled: true, exact_device_name: '', device_name_regexp: '', poll_interval: '2s' },
  server: { listen_address: '127.0.0.1:8080', read_timeout: '10s', write_timeout: '10s' },
  peer: { enabled: false, url: '<write-only-url>', reconnect_base: '500ms', reconnect_limit: '30s', stale_after: '500ms' },
  metrics: { namespace: 'musical_packets' },
  logging: { level: 'info', format: 'text' },
  rules: [],
}

export const snapshot: RuntimeSnapshot = {
  status: { state: 'ready', revision: 'public-revision', writable: true },
  config: { config: configuration, revision: '"public-revision"' },
  interfaces: {
    configured: 'auto', selected: 'en0', interfaces: [
      { name: 'en0', description: 'Ethernet', addresses: ['192.0.2.10/24'], up: true, loopback: false },
      { name: 'lo0', description: 'Loopback', addresses: ['127.0.0.1/8'], up: true, loopback: true },
    ],
  },
  midi: {
    enabled: true, discovery: 'ok', connected: true,
    current: { number: 2, name: 'USB Synth' }, devices: [{ number: 2, name: 'USB Synth' }],
  },
}

export const flowPage: FlowPage = {
  flows: [
    {
      id: '0123456789abcdef01234567', protocol: 'tcp',
      endpoint_a: { address: '192.0.2.10', port: 51820 },
      endpoint_b: { address: '198.51.100.20', port: 443 },
      first_seen: '2026-07-21T09:59:00Z', last_seen: '2026-07-21T10:00:00Z',
      packets: 240, bytes: 19200, packets_a_to_b: 140, packets_b_to_a: 100,
      muted: false, soloed: false, state: 'play', channel: 4,
      rule_id: 'web-traffic', rule_tier: 'user', mode: 'dorian', root: 2,
    },
    {
      id: 'fedcba9876543210fedcba98', protocol: 'udp',
      endpoint_a: { address: '2001:db8::1', port: 5353 },
      endpoint_b: { address: 'ff02::fb', port: 5353 },
      first_seen: '2026-07-21T09:58:00Z', last_seen: '2026-07-21T09:59:30Z',
      packets: 12, bytes: 980, packets_a_to_b: 12, packets_b_to_a: 0,
      muted: true, soloed: false, state: 'ignore', channel: 1,
      rule_tier: 'temporary_mute', mode: 'lydian', root: 7,
    },
  ],
  overlay: { muted: ['fedcba9876543210fedcba98'], soloed: [] },
  total: 2,
  limit: 500,
  truncated: false,
}

export function stubClient(overrides: Partial<ManagementClient> = {}): ManagementClient {
  return {
    getStatus: vi.fn().mockResolvedValue(snapshot.status),
    getConfig: vi.fn().mockResolvedValue(snapshot.config),
    getInterfaces: vi.fn().mockResolvedValue(snapshot.interfaces),
    getMIDI: vi.fn().mockResolvedValue(snapshot.midi),
    validateConfig: vi.fn().mockResolvedValue({ revision: 'public-revision', hot_fields: [], restart_required_fields: [] }),
    updateConfig: vi.fn().mockResolvedValue(snapshot.config),
    auditionMIDI: vi.fn().mockResolvedValue(undefined),
    panicMIDI: vi.fn().mockResolvedValue(undefined),
    getFlows: vi.fn().mockResolvedValue(flowPage),
    setMutedFlows: vi.fn().mockImplementation(async (flowIDs: string[]) => ({ muted: flowIDs, soloed: [] })),
    setSoloedFlows: vi.fn().mockImplementation(async (flowIDs: string[]) => ({ muted: [], soloed: flowIDs })),
    ...overrides,
  }
}
